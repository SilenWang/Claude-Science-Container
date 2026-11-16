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

// toolCallState 跟踪一个上游 tool_call 从增量到 Anthropic content block 的状态。
type toolCallState struct {
	upstreamIndex int
	id            string
	name          string
	// pending 是已收到、还没下发的 arguments 原始片段。绝不重新序列化。
	pending        strings.Builder
	anthropicIndex int
	started        bool
	stopped        bool
}

// streamState 把 OpenAI 增量拼成 Anthropic 内容块。
type streamState struct {
	requestID string
	model     string
	tr        *translation

	nextIndex   int
	textIndex   int
	textStarted bool
	textStopped bool

	// tools 按上游 tool_calls[].index 升序排列；nextOpenPos 指向第一个还没打开的块，
	// 保证 Anthropic 的 block index 严格按上游顺序分配（文本块占 0，工具从 1 起）。
	tools       []*toolCallState
	nextOpenPos int

	finishReason string
	outputTokens int
	finished     bool
}

func newStreamState(requestID, model string, tr *translation) *streamState {
	return &streamState{
		requestID: requestID,
		model:     model,
		tr:        tr,
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

// stopTextBlock 在第一个工具块开始前关闭文本块：Anthropic 的块按 index 顺序出现。
func (st *streamState) stopTextBlock(out *sseWriter) error {
	if !st.textStarted || st.textStopped {
		return nil
	}
	st.textStopped = true
	return out.event("content_block_stop", map[string]any{"index": st.textIndex})
}

// emitText 下发文本增量；文本块一旦关闭（后面接了工具块）就不再重开。
func (st *streamState) emitText(out *sseWriter, text string) error {
	if st.textStopped {
		return nil
	}
	if err := st.startTextBlock(out); err != nil {
		return err
	}
	return out.event("content_block_delta", map[string]any{
		"index": st.textIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

// consume 处理一条上游 data；返回是否收到 finish_reason、本帧是否带来增量内容。
func (st *streamState) consume(out *sseWriter, data string) (finished, activity bool, err error) {
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
			activity = true
			if err := st.emitText(out, text); err != nil {
				return false, activity, err
			}
		}
		if len(choice.Delta.ToolCalls) > 0 {
			activity = true
			for _, call := range choice.Delta.ToolCalls {
				st.addToolCall(call.Index, call.ID, call.Function.Name, call.Function.Arguments)
			}
			if err := st.pumpTools(out, false); err != nil {
				return false, activity, err
			}
		}
		if choice.FinishReason != nil {
			st.finishReason = *choice.FinishReason
			finished = true
		}
	}
	return finished, activity, nil
}

// toolState 取（或按上游 index 升序插入）一个 tool_call 的状态。
func (st *streamState) toolState(upstreamIndex int) *toolCallState {
	pos := sort.Search(len(st.tools), func(i int) bool {
		return st.tools[i].upstreamIndex >= upstreamIndex
	})
	if pos < len(st.tools) && st.tools[pos].upstreamIndex == upstreamIndex {
		return st.tools[pos]
	}
	call := &toolCallState{upstreamIndex: upstreamIndex}
	st.tools = append(st.tools, nil)
	copy(st.tools[pos+1:], st.tools[pos:])
	st.tools[pos] = call
	// 插到了已经处理过的位置之前：游标跟着右移，避免重复打开同一个块。
	if pos < st.nextOpenPos {
		st.nextOpenPos++
	}
	return call
}

func (st *streamState) addToolCall(index int, id, name, arguments string) {
	call := st.toolState(index)
	call.id = mergeDeltaField(call.id, id)
	call.name = mergeDeltaField(call.name, name)
	// 原样累积上游 arguments 片段，收尾/开块时原样作为 partial_json 下发。
	call.pending.WriteString(arguments)
}

// mergeDeltaField 容忍上游把 id/name 拆成多帧增量下发，也容忍每帧重复下发完整值。
func mergeDeltaField(current, incoming string) string {
	if incoming == "" || incoming == current {
		return current
	}
	if current == "" {
		return incoming
	}
	if strings.HasPrefix(incoming, current) {
		return incoming
	}
	if strings.HasPrefix(current, incoming) {
		return current
	}
	return incoming
}

// startToolBlock 打开一个 tool_use 块：id 原样透传上游 tool_call.id，name 回填 Anthropic 原名。
func (st *streamState) startToolBlock(out *sseWriter, call *toolCallState) error {
	if err := st.stopTextBlock(out); err != nil {
		return err
	}
	name := call.name
	if name == "" {
		name = "tool"
	}
	id := call.id
	if id == "" {
		id = "toolu_" + randomHex(12)
	}
	call.anthropicIndex = st.nextIndex
	st.nextIndex++
	call.started = true
	return out.event("content_block_start", map[string]any{
		"index": call.anthropicIndex,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  st.tr.originalToolName(name),
			"input": map[string]any{},
		},
	})
}

// pumpTools 按上游顺序打开工具块并下发已缓冲的 arguments 片段。
// force=false 时只打开「name/id 已到齐且已有参数」的块，保证上手就能真增量；
// 并行调用里靠后的工具会等前面的块先打开（block index 必须升序）。
func (st *streamState) pumpTools(out *sseWriter, force bool) error {
	for st.nextOpenPos < len(st.tools) {
		call := st.tools[st.nextOpenPos]
		if call.started {
			st.nextOpenPos++
			continue
		}
		ready := force || (call.name != "" && call.id != "" && call.pending.Len() > 0)
		if !ready {
			break
		}
		if err := st.startToolBlock(out, call); err != nil {
			return err
		}
		st.nextOpenPos++
	}

	// 已打开的块：新到的片段立刻作为 input_json_delta 原样下发。
	for _, call := range st.tools {
		if !call.started || call.pending.Len() == 0 {
			continue
		}
		partial := call.pending.String()
		call.pending.Reset()
		if err := out.event("content_block_delta", map[string]any{
			"index": call.anthropicIndex,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": partial},
		}); err != nil {
			return err
		}
	}

	if force {
		for _, call := range st.tools {
			if !call.started || call.stopped {
				continue
			}
			call.stopped = true
			if err := out.event("content_block_stop", map[string]any{"index": call.anthropicIndex}); err != nil {
				return err
			}
		}
	}
	return nil
}

// finish 按 Anthropic 顺序补齐 content_block_stop → message_delta → message_stop。
// 无论是正常结束、上游 EOF，还是上游没给 finish_reason，都走这里收尾。
func (st *streamState) finish(out *sseWriter) {
	if st.finished {
		return
	}
	st.finished = true

	// 没有任何内容时补一个空文本块，保持 M1/M2 的空回复行为。
	if !st.textStarted && len(st.tools) == 0 {
		_ = st.startTextBlock(out)
	}
	_ = st.stopTextBlock(out)
	_ = st.pumpTools(out, true)

	stopReason := mapFinishReason(st.finishReason)
	if len(st.tools) > 0 {
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
		finished, activity, consumeErr := state.consume(out, data)
		if consumeErr != nil {
			s.logf("[bridge] stream consume error: %v", consumeErr)
			emitUpstreamError(consumeErr.Error())
			return true
		}
		// 只有上游真有增量（文本或工具参数）才重置计时：ping 只在空闲时出现。
		if activity {
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
