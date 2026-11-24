package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// logCapture 收集 server.logf 的输出，用来断言 fallback 命中时那行日志。
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCapture) contains(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// captureUpstream 记录上游收到的请求体与 Authorization，再交给 handler 回包。
type captureUpstream struct {
	srv    *httptest.Server
	bodies chan map[string]any
	auths  chan string
}

func newCaptureUpstream(t *testing.T, handler http.HandlerFunc) *captureUpstream {
	t.Helper()
	c := &captureUpstream{
		bodies: make(chan map[string]any, 16),
		auths:  make(chan string, 16),
	}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		c.bodies <- body
		c.auths <- r.Header.Get("Authorization")
		handler(w, r)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// calls 返回上游已经收到的请求数。handler 在响应写回前就把请求体放进带缓冲的
// channel，因此 ServeHTTP 返回后这里的计数是准确的。
func (c *captureUpstream) calls() int {
	return len(c.bodies)
}

func (c *captureUpstream) last(t *testing.T) (map[string]any, string) {
	t.Helper()
	if len(c.bodies) == 0 {
		t.Fatalf("upstream %s received no request", c.srv.URL)
	}
	return <-c.bodies, <-c.auths
}

func replyJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, fakeOpenAIResponse)
}

func replySSE(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	frames := []string{
		`data: {"choices":[{"delta":{"content":"ok"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n",
		"data: [DONE]\n\n",
	}
	for _, frame := range frames {
		_, _ = io.WriteString(w, frame)
		flushIfPossible(w)
	}
}

const imageRequest = `{
	"model":"claude-haiku-4-5",
	"max_tokens":64,
	"messages":[{"role":"user","content":[
		{"type":"text","text":"这张图里是什么"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
	]}]
}`

const textRequest = `{
	"model":"claude-haiku-4-5",
	"max_tokens":64,
	"messages":[{"role":"user","content":"只回复两个字：收到"}]
}`

// fallbackTestConfig 造一份双上游配置：deepseek 承载纯文本 deepseek-flash，
// openai 承载 vision 模型 gpt-4o，两者用不同端口区分，便于断言到底发给了谁。
func fallbackTestConfig(dsURL, oaURL string, fallback ImageFallbackOptions) *config {
	return newConfigFromOptions(Options{
		DefaultBackend:    "deepseek",
		ForceModel:        "deepseek-flash",
		InlineImagePolicy: "omit_inline",
		ImageFallback:     fallback,
		ProxyHost:         "127.0.0.1",
		ProxyPort:         9876,
		Backends: []BackendOptions{
			{Name: "deepseek", APIKey: "ds-key", BaseURL: dsURL, UpstreamMode: "openai"},
			{Name: "openai", APIKey: "oa-key", BaseURL: oaURL, UpstreamMode: "openai"},
		},
	})
}

func newFallbackServer(t *testing.T, cfg *config) (*server, *logCapture) {
	t.Helper()
	logs := &logCapture{}
	srv := newServer(cfg, "test")
	srv.logf = logs.logf
	t.Cleanup(func() {
		srv.client.CloseIdleConnections()
		srv.streamClient.CloseIdleConnections()
	})
	return srv, logs
}

// userParts 取出上游请求里最后一条 user 消息的 content 片段。
func userParts(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	messages, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("upstream messages = %#v", body["messages"])
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok || msg["role"] != "user" {
			continue
		}
		switch content := msg["content"].(type) {
		case []any:
			parts := make([]map[string]any, 0, len(content))
			for _, item := range content {
				if part, ok := item.(map[string]any); ok {
					parts = append(parts, part)
				}
			}
			return parts
		case string:
			return []map[string]any{{"type": "text", "text": content}}
		}
	}
	t.Fatalf("no user message in %#v", messages)
	return nil
}

func hasPlaceholder(parts []map[string]any) bool {
	for _, part := range parts {
		if text, ok := part["text"].(string); ok && strings.Contains(text, "omitted by INLINE_IMAGE_POLICY") {
			return true
		}
	}
	return false
}

func imageURLs(parts []map[string]any) []string {
	var urls []string
	for _, part := range parts {
		if part["type"] != "image_url" {
			continue
		}
		if image, ok := part["image_url"].(map[string]any); ok {
			if url, ok := image["url"].(string); ok {
				urls = append(urls, url)
			}
		}
	}
	return urls
}

// TestImageFallbackRoutesToVisionBackend 覆盖非流式主路径：有图时整条请求
// 换成 fallback 后端 + fallback 模型，图片原样转发（不再丢弃），并留一行日志。
func TestImageFallbackRoutesToVisionBackend(t *testing.T) {
	ds := newCaptureUpstream(t, replyJSON)
	oa := newCaptureUpstream(t, replyJSON)

	cfg := fallbackTestConfig(ds.srv.URL, oa.srv.URL, ImageFallbackOptions{Mode: "auto", Backend: "openai", Model: "gpt-4o"})
	srv, logs := newFallbackServer(t, cfg)

	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", imageRequest)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if ds.calls() != 0 {
		t.Fatalf("primary backend received %d request(s); the image request must not reach the text-only model", ds.calls())
	}
	if oa.calls() != 1 {
		t.Fatalf("fallback backend received %d request(s), want 1", oa.calls())
	}
	body, auth := oa.last(t)
	if body["model"] != "gpt-4o" {
		t.Fatalf("fallback model = %#v", body["model"])
	}
	if auth != "Bearer oa-key" {
		t.Fatalf("fallback auth = %q", auth)
	}
	parts := userParts(t, body)
	urls := imageURLs(parts)
	if len(urls) != 1 || urls[0] != "data:image/png;base64,AAAA" {
		t.Fatalf("image parts = %#v", parts)
	}
	if hasPlaceholder(parts) {
		t.Fatalf("fallback request still carries an omit placeholder: %#v", parts)
	}
	if !logs.contains("image fallback: deepseek/deepseek-flash -> openai/gpt-4o") {
		t.Fatalf("missing fallback log line: %#v", logs.lines)
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// 回给 Claude 的 model 仍是请求里的模型，与 alias 的既有行为一致。
	if out["model"] != "claude-haiku-4-5" {
		t.Fatalf("response model = %#v", out["model"])
	}
}

// TestImageFallbackStreamRoutesToVisionBackend 覆盖流式路径：与上面同一判定，
// 上游同样看到 fallback 模型与原样转发的图片，SSE 帧完整。
func TestImageFallbackStreamRoutesToVisionBackend(t *testing.T) {
	ds := newCaptureUpstream(t, replySSE)
	oa := newCaptureUpstream(t, replySSE)

	cfg := fallbackTestConfig(ds.srv.URL, oa.srv.URL, ImageFallbackOptions{Mode: "auto", Backend: "openai", Model: "gpt-4o"})
	srv, logs := newFallbackServer(t, cfg)

	rec := doRequest(t, srv, http.MethodPost, "/v1/messages",
		strings.Replace(imageRequest, `"max_tokens":64,`, `"max_tokens":64,"stream":true,`, 1))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ds.calls() != 0 {
		t.Fatalf("primary backend received %d stream request(s)", ds.calls())
	}
	body, _ := oa.last(t)
	if body["model"] != "gpt-4o" {
		t.Fatalf("fallback stream model = %#v", body["model"])
	}
	if body["stream"] != true {
		t.Fatalf("fallback stream flag = %#v", body["stream"])
	}
	if urls := imageURLs(userParts(t, body)); len(urls) != 1 {
		t.Fatalf("fallback stream lost the image: %#v", userParts(t, body))
	}
	if !logs.contains("image fallback: deepseek/deepseek-flash -> openai/gpt-4o") {
		t.Fatalf("missing fallback log line: %#v", logs.lines)
	}

	frames := parseSSE(t, rec.Body.String())
	if !hasFrame(frames, "message_start") || !hasFrame(frames, "message_stop") {
		t.Fatalf("stream frames = %v", frameNames(frames))
	}
}

// TestImageFallbackLeavesTextRequestAlone 覆盖「无图不切换」：同一个 fallback
// 配置下，纯文本请求仍发给原来的后端与模型。
func TestImageFallbackLeavesTextRequestAlone(t *testing.T) {
	ds := newCaptureUpstream(t, replyJSON)
	oa := newCaptureUpstream(t, replyJSON)

	cfg := fallbackTestConfig(ds.srv.URL, oa.srv.URL, ImageFallbackOptions{Mode: "auto", Backend: "openai", Model: "gpt-4o"})
	srv, logs := newFallbackServer(t, cfg)

	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", textRequest)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if oa.calls() != 0 {
		t.Fatalf("fallback backend received a text-only request")
	}
	body, auth := ds.last(t)
	if body["model"] != "deepseek-flash" {
		t.Fatalf("primary model = %#v", body["model"])
	}
	if auth != "Bearer ds-key" {
		t.Fatalf("primary auth = %q", auth)
	}
	if logs.contains("image fallback") {
		t.Fatalf("text request logged a fallback: %#v", logs.lines)
	}
}

// TestImageFallbackUnconfiguredKeepsDropBehavior 覆盖「未配置 fallback 时保持
// 现有丢图行为」：没有 image_fallback 时，omit_inline 照旧丢内联图并补占位。
func TestImageFallbackUnconfiguredKeepsDropBehavior(t *testing.T) {
	ds := newCaptureUpstream(t, replyJSON)
	oa := newCaptureUpstream(t, replyJSON)

	cfg := fallbackTestConfig(ds.srv.URL, oa.srv.URL, ImageFallbackOptions{})
	srv, logs := newFallbackServer(t, cfg)

	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", imageRequest)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if oa.calls() != 0 {
		t.Fatalf("unconfigured fallback still rerouted the request")
	}
	body, _ := ds.last(t)
	if body["model"] != "deepseek-flash" {
		t.Fatalf("primary model = %#v", body["model"])
	}
	parts := userParts(t, body)
	if urls := imageURLs(parts); len(urls) != 0 {
		t.Fatalf("omit_inline forwarded the image: %#v", urls)
	}
	if !hasPlaceholder(parts) {
		t.Fatalf("missing omission note: %#v", parts)
	}
	if logs.contains("image fallback") {
		t.Fatalf("unconfigured fallback logged a hit: %#v", logs.lines)
	}
}

// TestImageFallbackTargetWithoutVisionStaysInert 覆盖发货默认值：fallback 指向
// 纯文本模型（deepseek-flash）时不转投，也不会既转投又丢图。
func TestImageFallbackTargetWithoutVisionStaysInert(t *testing.T) {
	ds := newCaptureUpstream(t, replyJSON)
	oa := newCaptureUpstream(t, replyJSON)

	for _, target := range []ImageFallbackOptions{
		{Mode: "auto", Backend: "deepseek", Model: "deepseek-flash"},
		{Mode: "auto", Backend: "openai", Model: "gpt-3.5-turbo"},
	} {
		cfg := fallbackTestConfig(ds.srv.URL, oa.srv.URL, target)
		srv, logs := newFallbackServer(t, cfg)

		rec := doRequest(t, srv, http.MethodPost, "/v1/messages", imageRequest)
		if rec.Code != http.StatusOK {
			t.Fatalf("%+v: status = %d, body = %s", target, rec.Code, rec.Body.String())
		}
		if logs.contains("image fallback") {
			t.Fatalf("%+v: text-only fallback target took over: %#v", target, logs.lines)
		}
		if status := cfg.resolveImageFallback(); status.backend != nil || status.reason == "" {
			t.Fatalf("%+v: resolution = %+v", target, status)
		}
	}
	if oa.calls() != 0 {
		t.Fatalf("text-only fallback target received %d request(s)", oa.calls())
	}
}

// TestPreservePolicyBeatsFallback 覆盖优先级：INLINE_IMAGE_POLICY=preserve 是
// 「照发原模型」的显式选择，fallback 不介入，哪怕当前模型看不了图。
func TestPreservePolicyBeatsFallback(t *testing.T) {
	ds := newCaptureUpstream(t, replyJSON)
	oa := newCaptureUpstream(t, replyJSON)

	cfg := fallbackTestConfig(ds.srv.URL, oa.srv.URL, ImageFallbackOptions{Mode: "auto", Backend: "openai", Model: "gpt-4o"})
	cfg.InlineImagePolicy = "preserve"
	srv, logs := newFallbackServer(t, cfg)

	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", imageRequest)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if oa.calls() != 0 {
		t.Fatalf("preserve policy still rerouted the request")
	}
	body, _ := ds.last(t)
	if body["model"] != "deepseek-flash" {
		t.Fatalf("primary model = %#v", body["model"])
	}
	if urls := imageURLs(userParts(t, body)); len(urls) != 1 {
		t.Fatalf("preserve did not forward the image: %#v", userParts(t, body))
	}
	if logs.contains("image fallback") {
		t.Fatalf("preserve logged a fallback: %#v", logs.lines)
	}
}

// TestAnthropicUpstreamNeverFallsBack 覆盖显式 upstream_mode=anthropic：这种
// 上游原生接收 Anthropic 载荷，图片直通，不能被 fallback 误转。
func TestAnthropicUpstreamNeverFallsBack(t *testing.T) {
	ds := newCaptureUpstream(t, replyJSON)
	oa := newCaptureUpstream(t, replyJSON)

	cfg := fallbackTestConfig(ds.srv.URL, oa.srv.URL, ImageFallbackOptions{Mode: "auto", Backend: "openai", Model: "gpt-4o"})
	cfg.backends["deepseek"].rawMode = "anthropic"
	srv, logs := newFallbackServer(t, cfg)

	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", imageRequest)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if oa.calls() != 0 {
		t.Fatalf("anthropic upstream was rerouted to the fallback backend")
	}
	body, _ := ds.last(t)
	if body["model"] != "deepseek-flash" {
		t.Fatalf("anthropic upstream model = %#v", body["model"])
	}
	// 不误转只保证「请求还留在原后端」；图片本身仍按 INLINE_IMAGE_POLICY
	// 处理（这里是 omit_inline，照旧丢弃），要原样发送需显式 preserve。
	if logs.contains("image fallback") {
		t.Fatalf("anthropic upstream logged a fallback: %#v", logs.lines)
	}
}

// TestImageInsideToolResultDoesNotTriggerFallback：tool_result 里的图会被拍平成
// 文本，bridge 转发不了；这种请求不该被当成「有图」而转投。
func TestImageInsideToolResultDoesNotTriggerFallback(t *testing.T) {
	ds := newCaptureUpstream(t, replyJSON)
	oa := newCaptureUpstream(t, replyJSON)

	cfg := fallbackTestConfig(ds.srv.URL, oa.srv.URL, ImageFallbackOptions{Mode: "auto", Backend: "openai", Model: "gpt-4o"})
	srv, logs := newFallbackServer(t, cfg)

	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{
		"model":"claude-haiku-4-5","max_tokens":64,
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"shot","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[
				{"type":"text","text":"screenshot"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
			]}]}
		]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if oa.calls() != 0 {
		t.Fatalf("image nested in tool_result triggered a fallback")
	}
	if logs.contains("image fallback") {
		t.Fatalf("tool_result image logged a fallback: %#v", logs.lines)
	}
}

func TestImageFallbackModeNormalization(t *testing.T) {
	cases := []struct {
		raw  string
		mode string
		ok   bool
	}{
		{"", "auto", true},
		{"auto", "auto", true},
		{"AUTO", "auto", true},
		{"on", "auto", true},
		{"off", "off", true},
		{"never", "off", true},
		{"preserve", "off", true},
		{"bogus", "off", false},
	}
	for _, tc := range cases {
		mode, ok := normalizeImageFallbackMode(tc.raw)
		if mode != tc.mode || ok != tc.ok {
			t.Errorf("normalizeImageFallbackMode(%q) = (%q, %v), want (%q, %v)", tc.raw, mode, ok, tc.mode, tc.ok)
		}
	}
}

func TestModelLooksVisionCapable(t *testing.T) {
	cases := map[string]bool{
		"gpt-4o":               true,
		"gpt-4o-mini":          true,
		"gpt-4.1":              true,
		"gpt-5":                true,
		"o3":                   true,
		"claude-sonnet-4-5":    true,
		"claude-3-5-haiku":     true,
		"gemini-2.5-pro":       true,
		"qwen-vl-max":          true,
		"qwen2.5-vl-72b":       true,
		"glm-4v":               true,
		"moonshot-vision-128k": true,
		"deepseek-vl2":         true,
		"deepseek-flash":       false,
		"deepseek-chat":        false,
		"deepseek-reasoner":    false,
		"gpt-3.5-turbo":        false,
		"":                     false,
	}
	for model, want := range cases {
		if got := modelLooksVisionCapable(model); got != want {
			t.Errorf("modelLooksVisionCapable(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestImageFallbackFromEnv(t *testing.T) {
	t.Setenv("IMAGE_FALLBACK_MODE", "off")
	t.Setenv("IMAGE_FALLBACK_BACKEND", "OpenAI")
	t.Setenv("IMAGE_FALLBACK_MODEL", "gpt-4o")
	cfg := newConfigFromEnv()
	if cfg.ImageFallback != (imageFallback{Mode: "off", Backend: "openai", Model: "gpt-4o"}) {
		t.Fatalf("fallback = %+v", cfg.ImageFallback)
	}

	// 未设置时沿用与原 compose 一致的默认值：deepseek/deepseek-flash 是纯文本，
	// 因此默认配置下 fallback 不会接管任何请求。
	for _, key := range []string{"IMAGE_FALLBACK_MODE", "IMAGE_FALLBACK_BACKEND", "IMAGE_FALLBACK_MODEL"} {
		t.Setenv(key, "")
	}
	cfg = newConfigFromEnv()
	if cfg.ImageFallback != (imageFallback{Mode: "auto", Backend: "deepseek", Model: "deepseek-flash"}) {
		t.Fatalf("default fallback = %+v", cfg.ImageFallback)
	}
	if resolution := cfg.resolveImageFallback(); resolution.backend != nil {
		t.Fatalf("default fallback is active: %+v", resolution)
	}

	// 拼错模式或后端名：告警并保持 fallback 不生效。
	t.Setenv("IMAGE_FALLBACK_MODE", "sometimes")
	t.Setenv("IMAGE_FALLBACK_BACKEND", "gemini")
	cfg = newConfigFromEnv()
	if cfg.ImageFallback.Mode != "off" || cfg.ImageFallback.Backend != "" {
		t.Fatalf("invalid fallback was accepted: %+v", cfg.ImageFallback)
	}
	if len(cfg.warnings) < 2 {
		t.Fatalf("warnings = %#v", cfg.warnings)
	}
}

func TestHealthReportsImageFallback(t *testing.T) {
	ds := newCaptureUpstream(t, replyJSON)
	oa := newCaptureUpstream(t, replyJSON)

	cfg := fallbackTestConfig(ds.srv.URL, oa.srv.URL, ImageFallbackOptions{Mode: "auto", Backend: "openai", Model: "gpt-4o"})
	srv, _ := newFallbackServer(t, cfg)
	rec := doRequest(t, srv, http.MethodGet, "/health", "")

	var out struct {
		ImageFallback struct {
			Mode    string `json:"mode"`
			Backend string `json:"backend"`
			Model   string `json:"model"`
			Active  bool   `json:"active"`
			Reason  string `json:"reason"`
		} `json:"image_fallback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !out.ImageFallback.Active || out.ImageFallback.Backend != "openai" || out.ImageFallback.Model != "gpt-4o" {
		t.Fatalf("health fallback = %+v", out.ImageFallback)
	}
	if out.ImageFallback.Reason != "" {
		t.Fatalf("active fallback carries a reason: %q", out.ImageFallback.Reason)
	}

	inactive := fallbackTestConfig(ds.srv.URL, oa.srv.URL, ImageFallbackOptions{Mode: "auto", Backend: "deepseek", Model: "deepseek-flash"})
	srv, _ = newFallbackServer(t, inactive)
	rec = doRequest(t, srv, http.MethodGet, "/health", "")
	out = struct {
		ImageFallback struct {
			Mode    string `json:"mode"`
			Backend string `json:"backend"`
			Model   string `json:"model"`
			Active  bool   `json:"active"`
			Reason  string `json:"reason"`
		} `json:"image_fallback"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if out.ImageFallback.Active || !strings.Contains(out.ImageFallback.Reason, "cannot read images") {
		t.Fatalf("inactive health fallback = %+v", out.ImageFallback)
	}
}
