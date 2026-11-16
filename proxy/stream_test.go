package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type sseFrame struct {
	name    string
	payload map[string]any
}

// parseSSE 把响应体拆成帧并解析 data。任何格式问题都直接失败。
func parseSSE(t *testing.T, body string) []sseFrame {
	t.Helper()
	var frames []sseFrame
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		name, data := "", ""
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if name == "" {
			t.Fatalf("frame without event name: %q", block)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("frame %s: invalid json %q: %v", name, data, err)
		}
		frames = append(frames, sseFrame{name: name, payload: payload})
	}
	return frames
}

func frameNames(frames []sseFrame) []string {
	names := make([]string, 0, len(frames))
	for _, f := range frames {
		names = append(names, f.name)
	}
	return names
}

func hasFrame(frames []sseFrame, name string) bool {
	for _, f := range frames {
		if f.name == name {
			return true
		}
	}
	return false
}

func flushIfPossible(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func TestStreamEventSequence(t *testing.T) {
	bodies := make(chan map[string]any, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies <- body

		w.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"从"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"一"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":2}}` + "\n\n",
			"data: [DONE]\n\n",
		}

		// 第 2 帧从 JSON 中间切成两块写入，覆盖"一个帧被拆成两个 chunk"的情况。
		cut := strings.Index(frames[1], `"content"`)
		if cut < 0 {
			t.Errorf("cannot find split point in %q", frames[1])
			return
		}
		_, _ = io.WriteString(w, frames[0])
		flushIfPossible(w)
		_, _ = io.WriteString(w, frames[1][:cut])
		flushIfPossible(w)
		_, _ = io.WriteString(w, frames[1][cut:])
		flushIfPossible(w)
		for _, frame := range frames[2:] {
			_, _ = io.WriteString(w, frame)
			flushIfPossible(w)
		}
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{
		"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,
		"messages":[{"role":"user","content":"从一数到五"}]
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("cache-control = %q", cc)
	}

	upstreamBody := <-bodies
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream stream = %#v", upstreamBody["stream"])
	}

	frames := parseSSE(t, rec.Body.String())
	wantNames := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	gotNames := frameNames(frames)
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("event order = %v, want %v", gotNames, wantNames)
	}
	for _, f := range frames {
		if f.payload["type"] != f.name {
			t.Fatalf("event %s carries type %#v", f.name, f.payload["type"])
		}
	}

	start := frames[0].payload["message"].(map[string]any)
	if id, _ := start["id"].(string); !strings.HasPrefix(id, "msg_") {
		t.Fatalf("message id = %#v", start["id"])
	}
	if start["type"] != "message" || start["role"] != "assistant" || start["model"] != "claude-sonnet-4-5" {
		t.Fatalf("message_start.message = %#v", start)
	}
	usage := start["usage"].(map[string]any)
	if _, ok := usage["input_tokens"]; !ok {
		t.Fatalf("message_start usage = %#v", usage)
	}

	blockStart := frames[1].payload
	if blockStart["index"] != float64(0) {
		t.Fatalf("content_block_start index = %#v", blockStart["index"])
	}
	block := blockStart["content_block"].(map[string]any)
	if block["type"] != "text" || block["text"] != "" {
		t.Fatalf("content_block_start block = %#v", block)
	}

	var text strings.Builder
	for _, f := range frames[2:4] {
		if f.payload["index"] != float64(0) {
			t.Fatalf("delta index = %#v", f.payload["index"])
		}
		delta := f.payload["delta"].(map[string]any)
		if delta["type"] != "text_delta" {
			t.Fatalf("delta type = %#v", delta["type"])
		}
		text.WriteString(delta["text"].(string))
	}
	if text.String() != "从一" {
		t.Fatalf("streamed text = %q", text.String())
	}

	messageDelta := frames[5].payload
	delta := messageDelta["delta"].(map[string]any)
	if delta["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %#v", delta["stop_reason"])
	}
	if _, ok := delta["stop_sequence"]; !ok {
		t.Fatalf("message_delta missing stop_sequence: %#v", delta)
	}
	usage = messageDelta["usage"].(map[string]any)
	if usage["output_tokens"] != float64(2) {
		t.Fatalf("output_tokens = %#v", usage["output_tokens"])
	}
}

func TestStreamPingHeartbeat(t *testing.T) {
	old := ssePingInterval
	ssePingInterval = 20 * time.Millisecond
	t.Cleanup(func() { ssePingInterval = old })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"甲"}}]}`+"\n\n")
		flushIfPossible(w)
		// 静默远超过 ping 间隔，中间应该冒出 ping。
		time.Sleep(120 * time.Millisecond)
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"乙"}}]}`+"\n\n")
		flushIfPossible(w)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flushIfPossible(w)
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{
		"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,
		"messages":[{"role":"user","content":"从一数到五"}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	frames := parseSSE(t, rec.Body.String())
	pings := 0
	firstDelta, firstPing, stop := -1, -1, -1
	for i, f := range frames {
		switch f.name {
		case "ping":
			pings++
			if firstPing < 0 {
				firstPing = i
			}
			if f.payload["type"] != "ping" {
				t.Fatalf("ping payload = %#v", f.payload)
			}
		case "content_block_delta":
			if firstDelta < 0 {
				firstDelta = i
			}
		case "content_block_stop":
			stop = i
		}
	}
	if pings == 0 {
		t.Fatalf("no ping frame in %v", frameNames(frames))
	}
	if !(firstDelta >= 0 && firstDelta < firstPing && firstPing < stop) {
		t.Fatalf("ping order wrong: delta=%d ping=%d stop=%d frames=%v", firstDelta, firstPing, stop, frameNames(frames))
	}
}

func TestStreamMidStreamDisconnect(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"半"}}]}`+"\n\n")
		flushIfPossible(w)
		time.Sleep(10 * time.Millisecond)

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("upstream response writer does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack upstream conn: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{
		"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,
		"messages":[{"role":"user","content":"从一数到五"}]
	}`)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, body)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if strings.Contains(body, "api_error") || strings.Contains(body, `"error":{"type":"api_error"`) {
		t.Fatalf("upstream failure leaked into stream: %s", body)
	}

	frames := parseSSE(t, body)
	if len(frames) == 0 {
		t.Fatalf("empty stream")
	}
	if frames[len(frames)-1].name != "message_stop" {
		t.Fatalf("stream must end with message_stop, got %v", frameNames(frames))
	}
	// 已进入 SSE 后上游中断：用 error 事件收尾，绝不把 502/HTML 写进流。
	if !hasFrame(frames, "error") {
		t.Fatalf("missing error frame: %v", frameNames(frames))
	}
	for _, f := range frames {
		if f.name == "error" {
			errObj, ok := f.payload["error"].(map[string]any)
			if !ok || errObj["type"] != "upstream_error" {
				t.Fatalf("error payload = %#v", f.payload)
			}
		}
	}
}

// 上游正常 EOF 但没给 [DONE]/finish_reason：补齐 content_block_stop → message_delta → message_stop。
func TestStreamUpstreamEOFFinishesMessage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"未完"}}]}`+"\n\n")
		flushIfPossible(w)
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{
		"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,
		"messages":[{"role":"user","content":"hi"}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	frames := parseSSE(t, rec.Body.String())
	names := frameNames(frames)
	tail := names[len(names)-3:]
	want := []string{"content_block_stop", "message_delta", "message_stop"}
	if strings.Join(tail, ",") != strings.Join(want, ",") {
		t.Fatalf("tail = %v, want %v (frames = %v)", tail, want, names)
	}
}

func TestStreamFailsBeforeFirstEvent(t *testing.T) {
	t.Run("upstream status", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
		}))
		defer upstream.Close()

		srv, _ := newTestServer(t, upstream.URL)
		rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{
			"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,
			"messages":[{"role":"user","content":"hi"}]
		}`)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
		}
	})

	t.Run("empty upstream stream", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
		}))
		defer upstream.Close()

		srv, _ := newTestServer(t, upstream.URL)
		rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{
			"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,
			"messages":[{"role":"user","content":"hi"}]
		}`)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("body is not json: %v (%s)", err, rec.Body.String())
		}
		if out["type"] != "error" {
			t.Fatalf("envelope = %#v", out)
		}
	})
}
