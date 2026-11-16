package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, upstreamURL string) (*server, *config) {
	t.Helper()
	cfg := testConfig("deepseek", upstreamURL)
	srv := newServer(cfg, "test")
	srv.logf = t.Logf
	t.Cleanup(func() {
		srv.client.CloseIdleConnections()
		srv.streamClient.CloseIdleConnections()
	})
	return srv, cfg
}

func doRequest(t *testing.T, srv *server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

const fakeOpenAIResponse = `{
	"id":"chatcmpl-1",
	"choices":[{"message":{"role":"assistant","content":"收到"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":9,"completion_tokens":2}
}`

func TestMessagesEndpoint(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fakeOpenAIResponse)
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{
		"model":"claude-sonnet-4-5",
		"max_tokens":64,
		"system":"be brief",
		"messages":[{"role":"user","content":"只回复两个字：收到"}]
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("upstream auth = %q", gotAuth)
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("upstream messages = %#v", gotBody["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" {
		t.Fatalf("upstream system = %#v", first)
	}
	if _, ok := gotBody["stream"]; ok {
		t.Fatalf("M1 must not send stream to upstream: %#v", gotBody["stream"])
	}
	if gotBody["max_tokens"] != float64(64) {
		t.Fatalf("max_tokens = %#v", gotBody["max_tokens"])
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out["type"] != "message" || out["role"] != "assistant" {
		t.Fatalf("response envelope = %#v", out)
	}
	if out["model"] != "claude-sonnet-4-5" {
		t.Fatalf("response model = %#v", out["model"])
	}
	if out["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %#v", out["stop_reason"])
	}
	blocks := out["content"].([]any)
	text := blocks[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "收到" {
		t.Fatalf("content = %#v", out["content"])
	}
	usage := out["usage"].(map[string]any)
	if usage["input_tokens"] != float64(9) || usage["output_tokens"] != float64(2) {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestPathNormalization(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, fakeOpenAIResponse)
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	for _, path := range []string{"/messages", "/v1/messages", "/v1/v1/messages"} {
		rec := doRequest(t, srv, http.MethodPost, path, `{"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}

	if got := normalizePath("/v1/v1/messages"); got != "/v1/messages" {
		t.Errorf("normalizePath = %q", got)
	}
	if got := normalizePath("/health"); got != "/health" {
		t.Errorf("normalizePath(/health) = %q", got)
	}
}

func TestCountTokens(t *testing.T) {
	srv, _ := newTestServer(t, "http://127.0.0.1:1")
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages/count_tokens", `{
		"system":"abc",
		"messages":[{"role":"user","content":"`+strings.Repeat("a", 37)+`"}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["input_tokens"] != float64(10) { // (3+37)/4 = 10
		t.Fatalf("input_tokens = %#v", out["input_tokens"])
	}

	empty := doRequest(t, srv, http.MethodPost, "/v1/messages/count_tokens", `{"messages":[]}`)
	var emptyOut map[string]any
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyOut); err != nil {
		t.Fatal(err)
	}
	if emptyOut["input_tokens"] != float64(1) {
		t.Fatalf("empty input_tokens = %#v", emptyOut["input_tokens"])
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, "http://127.0.0.1:1")
	rec := doRequest(t, srv, http.MethodGet, "/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("health body = %s", rec.Body.String())
	}
}

func TestMockEndpoints(t *testing.T) {
	srv, cfg := newTestServer(t, "http://127.0.0.1:1")
	cfg.ModelAliases = []modelAlias{{ID: "claude-haiku-4-5", DisplayName: "DeepSeek V4 Flash", Backend: "deepseek", Model: "deepseek-v4-flash-vision-exp"}}
	cfg.ModelListMode = "aliases"

	models := doRequest(t, srv, http.MethodGet, "/v1/models", "")
	if models.Code != http.StatusOK {
		t.Fatalf("models status = %d", models.Code)
	}
	var modelsOut map[string]any
	if err := json.Unmarshal(models.Body.Bytes(), &modelsOut); err != nil {
		t.Fatal(err)
	}
	data := modelsOut["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("models data = %#v", data)
	}
	first := data[0].(map[string]any)
	if first["id"] != "claude-haiku-4-5" || first["display_name"] != "DeepSeek V4 Flash" || first["type"] != "model" {
		t.Fatalf("model entry = %#v", first)
	}
	if modelsOut["first_id"] != "claude-haiku-4-5" || modelsOut["last_id"] != "claude-haiku-4-5" || modelsOut["has_more"] != false {
		t.Fatalf("models envelope = %#v", modelsOut)
	}

	for _, path := range []string{"/v1/userinfo", "/v1/me", "/v1/user", "/v1/profile", "/v1/account"} {
		rec := doRequest(t, srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "byok@localhost") {
			t.Errorf("%s: status = %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}

	orgs := doRequest(t, srv, http.MethodGet, "/v1/organizations", "")
	if orgs.Code != http.StatusOK || !strings.Contains(orgs.Body.String(), "BYOK Organization") {
		t.Fatalf("organizations: status = %d, body = %s", orgs.Code, orgs.Body.String())
	}

	oauth := doRequest(t, srv, http.MethodPost, "/v1/oauth/token", "")
	if oauth.Code != http.StatusOK || !strings.Contains(oauth.Body.String(), "fake-bearer-token-for-proxy") {
		t.Fatalf("oauth: status = %d, body = %s", oauth.Code, oauth.Body.String())
	}

	unknown := doRequest(t, srv, http.MethodGet, "/v1/some/unknown", "")
	if unknown.Code != http.StatusOK || !strings.Contains(unknown.Body.String(), `"ok":true`) {
		t.Fatalf("catch-all: status = %d, body = %s", unknown.Code, unknown.Body.String())
	}
}

func TestUpstreamErrorIsForwarded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["type"] != "error" {
		t.Fatalf("error envelope = %#v", out)
	}
	if !strings.Contains(rec.Body.String(), "rate limited") {
		t.Fatalf("error body = %s", rec.Body.String())
	}
}

func TestMissingAPIKeyReturns400(t *testing.T) {
	srv, cfg := newTestServer(t, "http://127.0.0.1:1")
	cfg.backends["deepseek"].apiKey = ""
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no API key configured") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestAliasRoutesToConfiguredBackend(t *testing.T) {
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, fakeOpenAIResponse)
	}))
	defer upstream.Close()

	srv, cfg := newTestServer(t, upstream.URL)
	cfg.backends["custom"].baseURL = upstream.URL + "/v1"
	cfg.ModelAliases = []modelAlias{{ID: "claude-opus-4-8", DisplayName: "Custom", Backend: "custom", Model: "custom-model"}}

	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{"model":"claude-opus-4-8","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if hits != 1 {
		t.Fatalf("custom backend hits = %d", hits)
	}
}

func TestNonV1PathFallsBackToCatchAll(t *testing.T) {
	// 上游行为：任何路径都会被归一化成 /v1/*，由兜底返回 {"ok":true}。
	srv, _ := newTestServer(t, "http://127.0.0.1:1")
	rec := doRequest(t, srv, http.MethodGet, "/not-a-route", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
