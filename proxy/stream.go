package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ssePingInterval 是上游静默（没有下发 delta）多久后补一条 ping。
// 单测会把它改小到几十毫秒，避免真的等 15 秒。
var ssePingInterval = 15 * time.Second

// sseMaxLineSize 是单条 SSE 行的上限；图片相关的 delta 可能较大。
const sseMaxLineSize = 1 << 20

// sseWriter 负责写出 Anthropic 风格的 SSE 帧：
//
//	event: <type>
//	data: <json>
//	<空行>
//
// 每个帧结束都立即 flush，保证 CS 能逐字渲染。
type sseWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	return &sseWriter{w: w, rc: http.NewResponseController(w)}
}

// begin 写响应头并立即 flush。调用后不能再返回 JSON 错误。
func (s *sseWriter) begin() {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// 让 nginx 之类的前置代理不要缓冲。
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	_ = s.rc.Flush()
}

func (s *sseWriter) event(name string, fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	// Anthropic 的每个事件 data 里都带 type，且与 event 名一致。
	fields["type"] = name
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, payload); err != nil {
		return err
	}
	return s.rc.Flush()
}

// openAIStreamChunk 是上游 SSE 的单条 data。
type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type toolCallState struct {
	id   string
	name string
	args strings.Builder
}

// streamState 把 OpenAI 增量拼成 Anthropic 内容块。
type streamState struct {
	requestID string
	model     string
	tr        *translation

	nextIndex   int
	textIndex   int
	textStarted bool

	toolCalls map[int]*toolCallState

	finishReason string
	outputTokens int
}

func newStreamState(requestID, model string, tr *translation) *streamState {
	return &streamState{
		requestID: requestID,
		model:     model,
		tr:        tr,
		toolCalls: map[int]*toolCallState{},
	}
}

func (st *streamState) messageStart() map[string]any {
	return map[string]any{
		"id":            st.requestID,
		"type":          "message",
		"role":          "assistant",
		"model":         st.model,
		"content":       []any{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
	}
}

func (st *streamState) startTextBlock(out *sseWriter) error {
	if st.textStarted {
		return nil
	}
	st.textIndex = st.nextIndex
	st.nextIndex++
	st.textStarted = true
	return out.event("content_block_start", map[string]any{
		"index":         st.textIndex,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

// consume 处理一条上游 data；返回是否收到 finish_reason、是否下发了文本增量。
func (st *streamState) consume(out *sseWriter, data string) (finished, emittedDelta bool, err error) {
	var chunk openAIStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return false, false, fmt.Errorf("invalid JSON in upstream stream: %w", err)
	}
	if chunk.Error != nil {
		message := chunk.Error.Message
		if message == "" {
			message = chunk.Error.Type
		}
		if message == "" {
			message = "unknown upstream error"
		}
		return false, false, fmt.Errorf("upstream stream error: %s", message)
	}
	if chunk.Usage != nil {
		st.outputTokens = chunk.Usage.CompletionTokens
	}

	for _, choice := range chunk.Choices {
		if text := openAIContentText(choice.Delta.Content); text != "" {
			if err := st.startTextBlock(out); err != nil {
				return false, false, err
			}
			if err := out.event("content_block_delta", map[string]any{
				"index": st.textIndex,
				"delta": map[string]any{"type": "text_delta", "text": text},
			}); err != nil {
				return false, false, err
			}
			emittedDelta = true
		}
		for _, call := range choice.Delta.ToolCalls {
			st.addToolCall(call.Index, call.ID, call.Function.Name, call.Function.Arguments)
		}
		if choice.FinishReason != nil {
			st.finishReason = *choice.FinishReason
			finished = true
		}
	}
	return finished, emittedDelta, nil
}

func (st *streamState) addToolCall(index int, id, name, arguments string) {
	call, ok := st.toolCalls[index]
	if !ok {
		call = &toolCallState{}
		st.toolCalls[index] = call
	}
	if id != "" {
		call.id = id
	}
	if name != "" {
		call.name = name
	}
	call.args.WriteString(arguments)
}

// finish 按 Anthropic 顺序补齐 content_block_stop → message_delta → message_stop。
// 无论是正常结束、上游 EOF，还是上游没给 finish_reason，都走这里收尾。
func (st *streamState) finish(out *sseWriter) {
	if st.textStarted || len(st.toolCalls) == 0 {
		if err := st.startTextBlock(out); err == nil {
			_ = out.event("content_block_stop", map[string]any{"index": st.textIndex})
		}
	}

	indexes := make([]int, 0, len(st.toolCalls))
	for index := range st.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, key := range indexes {
		call := st.toolCalls[key]
		index := st.nextIndex
		st.nextIndex++

		name := call.name
		if original, ok := st.tr.toolNames[name]; ok && original != "" {
			name = original
		}
		id := call.id
		if id == "" {
			id = "toolu_" + randomHex(12)
		}
		_ = out.event("content_block_start", map[string]any{
			"index": index,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": map[string]any{},
			},
		})
		if args := call.args.String(); args != "" {
			_ = out.event("content_block_delta", map[string]any{
				"index": index,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
			})
		}
		_ = out.event("content_block_stop", map[string]any{"index": index})
	}

	stopReason := mapFinishReason(st.finishReason)
	if len(st.toolCalls) > 0 {
		stopReason = "tool_use"
	}
	_ = out.event("message_delta", map[string]any{
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": st.outputTokens},
	})
	_ = out.event("message_stop", map[string]any{})
}

// readSSEData 读取下一条 data 行，跳过空行、注释与 event: 行。
// 返回 "[DONE]" 表示上游流结束。
func readSSEData(scanner *bufio.Scanner) (string, error) {
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "", strings.HasPrefix(line, ":"), strings.HasPrefix(line, "event:"):
			continue
		case strings.HasPrefix(line, "data:"):
			return strings.TrimSpace(strings.TrimPrefix(line, "data:")), nil
		case strings.HasPrefix(line, "{"):
			// 容错：个别上游直接发裸 JSON 行。
			return line, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

func resetSSETimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

type upstreamChunk struct {
	data string
	err  error
}

// openUpstreamStream 建立上游流式请求。这里不读 body，交给 handler 逐行消费。
func (s *server) openUpstreamStream(ctx context.Context, b *backend, payload map[string]any) (*http.Response, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(b.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	return s.streamClient.Do(httpReq)
}

// handleMessagesStream 把上游 OpenAI 流翻译成 Anthropic SSE。
func (s *server) handleMessagesStream(w http.ResponseWriter, r *http.Request, req *anthropicRequest, b *backend, payload map[string]any, requestID string, tr *translation) {
	httpResp, err := s.openUpstreamStream(r.Context(), b, payload)
	if err != nil {
		s.logf("[bridge] stream upstream error: %v", err)
		writeAPIError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBody))
		s.logf("[bridge] stream upstream returned %d: %s", httpResp.StatusCode, snippet(body))
		writeAPIError(w, httpResp.StatusCode, "api_error",
			fmt.Sprintf("backend returned %d: %s", httpResp.StatusCode, snippet(body)))
		return
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), sseMaxLineSize)

	// 先拿到第一条 data 再写 SSE 头：此时失败还能干净地返回 502 JSON。
	first, err := readSSEData(scanner)
	if err != nil {
		s.logf("[bridge] stream ended before first event: %v", err)
		writeAPIError(w, http.StatusBadGateway, "api_error",
			"backend stream ended before first event: "+err.Error())
		return
	}

	out := newSSEWriter(w)
	out.begin()
	state := newStreamState(requestID, req.Model, tr)
	if err := out.event("message_start", map[string]any{"message": state.messageStart()}); err != nil {
		return
	}

	done := make(chan struct{})
	defer close(done)

	chunks := make(chan upstreamChunk, 8)
	go func() {
		for {
			data, readErr := readSSEData(scanner)
			select {
			case chunks <- upstreamChunk{data: data, err: readErr}:
			case <-done:
				return
			}
			if readErr != nil || data == "[DONE]" {
				return
			}
		}
	}()

	emitUpstreamError := func(message string) {
		_ = out.event("error", map[string]any{
			"error": map[string]any{"type": "upstream_error", "message": message},
		})
		_ = out.event("message_stop", map[string]any{})
	}

	timer := time.NewTimer(ssePingInterval)
	defer timer.Stop()

	handle := func(data string) bool {
		if data == "[DONE]" {
			state.finish(out)
			return true
		}
		finished, delta, consumeErr := state.consume(out, data)
		if consumeErr != nil {
			s.logf("[bridge] stream consume error: %v", consumeErr)
			emitUpstreamError(consumeErr.Error())
			return true
		}
		if delta {
			resetSSETimer(timer, ssePingInterval)
		}
		if finished {
			state.finish(out)
			return true
		}
		return false
	}

	if handle(first) {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
			if err := out.event("ping", map[string]any{}); err != nil {
				return
			}
			timer.Reset(ssePingInterval)
		case chunk := <-chunks:
			if chunk.err != nil {
				if errors.Is(chunk.err, io.EOF) {
					// 上游 EOF 但没给 [DONE]/finish_reason：照常补齐收尾。
					state.finish(out)
				} else {
					s.logf("[bridge] stream read error: %v", chunk.err)
					emitUpstreamError(chunk.err.Error())
				}
				return
			}
			if handle(chunk.data) {
				return
			}
		}
	}
}
