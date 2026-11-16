package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxRequestBody  = 32 << 20 // 32 MiB：图片请求可能较大
	maxResponseBody = 32 << 20
	maxErrorSnippet = 500
)

// server 是 M1/M2 的全部 HTTP 入口。
type server struct {
	cfg          *config
	client       *http.Client
	streamClient *http.Client
	version      string
	logf         func(format string, args ...any)

	// warnMu/warnedModels 让 FORCE_MODEL 回落告警对同一 requested -> forced
	// 组合只打一次，避免每个请求刷屏。
	warnMu       sync.Mutex
	warnedModels map[string]bool
}

func newServer(cfg *config, version string) *server {
	return &server{
		cfg:          cfg,
		client:       &http.Client{Timeout: 120 * time.Second},
		streamClient: newStreamClient(),
		version:      version,
		logf:         log.Printf,
		warnedModels: map[string]bool{},
	}
}

// newStreamClient 用于 SSE：不能设置总超时，否则长回答会被截断；
// 只在等待上游响应头时设一个上限。
func newStreamClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 90 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: transport}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := normalizePath(r.URL.Path)

	switch {
	case path == "/health":
		s.handleHealth(w, r)
	case path == "/v1/messages":
		s.handleMessages(w, r)
	case path == "/v1/messages/count_tokens":
		s.handleCountTokens(w, r)
	case path == "/v1/models":
		s.handleModels(w, r)
	case path == "/v1/userinfo" || path == "/v1/me" || path == "/v1/user" ||
		path == "/v1/profile" || path == "/v1/account":
		writeJSON(w, http.StatusOK, fakeUser())
	case path == "/v1/organizations":
		writeJSON(w, http.StatusOK, fakeOrgList())
	case path == "/v1/organization" || strings.HasPrefix(path, "/v1/organizations/"):
		writeJSON(w, http.StatusOK, fakeOrg())
	case strings.HasPrefix(path, "/v1/"):
		s.handleCatchAll(w, r, path)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found_error", "unknown path: "+r.URL.Path)
	}
}

// normalizePath 复刻上游的路径归一化：/messages → /v1/messages，
// /v1/v1/... 压平，base URL 带不带 /v1 都能用。
func normalizePath(path string) string {
	if path == "" {
		path = "/"
	}
	switch path {
	case "/health", "/docs", "/openapi.json", "/favicon.ico":
		return path
	}
	for strings.Contains(path, "/v1/v1/") {
		path = strings.Replace(path, "/v1/v1/", "/v1/", 1)
	}
	if path == "/v1/v1" {
		path = "/v1"
	}
	if path != "/v1" && !strings.HasPrefix(path, "/v1/") {
		path = "/v1" + path
	}
	// 尾斜杠归一化：/v1/models/ 与 /v1/models 落同一条注册路由，
	// /v1/messages/ 也不会再被兜底句柄吞掉。
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	if path == "" {
		path = "/"
	}
	return path
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, r)
		return
	}
	modes := map[string]string{}
	configured := map[string]bool{}
	for name, b := range s.cfg.backends {
		modes[name] = normalizeUpstreamMode(b.rawMode)
		configured[name] = b.apiKey != "" && b.baseURL != ""
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "ok",
		"version":             s.version,
		"default_backend":     s.cfg.DefaultBackend,
		"force_model":         orNone(s.cfg.ForceModel),
		"model_list_mode":     s.cfg.ModelListMode,
		"inline_image_policy": s.cfg.InlineImagePolicy,
		"model_aliases":       len(s.cfg.ModelAliases),
		"backends_configured": configured,
		"upstream_modes":      modes,
	})
}

func (s *server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r)
		return
	}

	body, err := readJSONObject(w, r)
	if err != nil {
		return
	}

	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request_error", "invalid Messages request: "+err.Error())
		return
	}
	if req.Model == "" {
		req.Model = "claude-sonnet-4-5"
	}

	b, backendModel, err := s.cfg.resolve(req.Model)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.warnForcedModel(req.Model, backendModel)

	openaiBody, tr, err := anthropicToOpenAI(&req, backendModel, s.cfg, b.name)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	requestID := "msg_" + randomHex(8)
	s.logf("[bridge] -> %s | model=%s | stream=%t | original_model=%s",
		b.name, backendModel, req.Stream, req.Model)

	if req.Stream {
		s.handleMessagesStream(w, r, &req, b, openaiBody, requestID, tr)
		return
	}

	resp, status, err := s.callUpstream(b, openaiBody)
	if err != nil {
		s.logf("[bridge] upstream error: %v", err)
		writeAPIError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	if status != http.StatusOK {
		s.logf("[bridge] upstream returned %d: %s", status, snippet(resp))
		writeAPIError(w, status, "api_error", "backend returned "+fmt.Sprint(status)+": "+snippet(resp))
		return
	}

	var parsed openAIResponse
	if err := json.Unmarshal(resp, &parsed); err != nil {
		writeAPIError(w, http.StatusBadGateway, "api_error", "invalid JSON from backend: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, openAIToAnthropic(&parsed, req.Model, requestID, tr))
}

func (s *server) callUpstream(b *backend, payload map[string]any) ([]byte, int, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	url := strings.TrimRight(b.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)

	httpResp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer httpResp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBody))
	if err != nil {
		return nil, 0, err
	}
	return data, httpResp.StatusCode, nil
}

func (s *server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r)
		return
	}
	body, err := readJSONObject(w, r)
	if err != nil {
		return
	}
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request_error", "invalid token request: "+err.Error())
		return
	}

	total := utf8.RuneCountInString(systemText(req.System))
	for _, msg := range req.Messages {
		text, blocks, isText := decodeBlocks(msg.Content)
		if isText {
			total += utf8.RuneCountInString(text)
			continue
		}
		for _, block := range blocks {
			if encoded, err := json.Marshal(block); err == nil {
				total += utf8.RuneCount(encoded)
			}
		}
	}

	tokens := total / 4
	if tokens < 1 {
		tokens = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": tokens})
}

// warnForcedModel 在请求模型没有 alias 映射、最终被 FORCE_MODEL 顶替时打一条
// 警告。内置模型（如 gpt-4o）同样会走这条回落，因此也在这里留下痕迹，不静默。
// 同一 requested -> forced 组合只打一次。
func (s *server) warnForcedModel(requested, backendModel string) {
	forced := strings.TrimSpace(s.cfg.ForceModel)
	if forced == "" || backendModel != forced || requested == forced {
		return
	}
	if s.cfg.aliasFor(requested) != nil {
		return
	}
	key := requested + " -> " + forced
	s.warnMu.Lock()
	if s.warnedModels[key] {
		s.warnMu.Unlock()
		return
	}
	s.warnedModels[key] = true
	s.warnMu.Unlock()
	s.logf("[bridge] WARNING: requested model %q has no MODEL_ALIASES mapping and was replaced by FORCE_MODEL %q", requested, forced)
}

func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, r)
		return
	}
	models := s.cfg.modelList()
	// 空清单时 first_id/last_id 必须是 null（不是 ""），与 Anthropic 的列表语义一致。
	var firstID, lastID any
	if len(models) > 0 {
		firstID = fmt.Sprint(models[0]["id"])
		lastID = fmt.Sprint(models[len(models)-1]["id"])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":     models,
		"has_more": false,
		"first_id": firstID,
		"last_id":  lastID,
	})
}

// handleCatchAll 兜住 CS 仍在调用的其它 /v1 接口，避免 404 打断会话。
func (s *server) handleCatchAll(w http.ResponseWriter, r *http.Request, path string) {
	lowered := strings.ToLower(path)
	switch {
	case strings.Contains(lowered, "oauth") || strings.Contains(lowered, "token"):
		writeJSON(w, http.StatusOK, fakeToken())
	case strings.Contains(lowered, "organization") || pathHasSegment(lowered, "org", "orgs"):
		writeJSON(w, http.StatusOK, fakeOrgList())
	case pathHasSegment(lowered, "userinfo", "profile", "account", "user", "me"):
		writeJSON(w, http.StatusOK, fakeUser())
	default:
		s.logf("[bridge] catch-all %s %s -> ok", r.Method, path)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func pathHasSegment(path string, names ...string) bool {
	for _, segment := range strings.Split(path, "/") {
		for _, name := range names {
			if segment == name {
				return true
			}
		}
	}
	return false
}

func readJSONObject(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body: "+err.Error())
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		err := fmt.Errorf("request body must be a JSON object")
		writeAPIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	var probe map[string]any
	if err := json.Unmarshal(body, &probe); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request_error", "request body must be valid JSON: "+err.Error())
		return nil, err
	}
	return body, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("[bridge] failed to write response: %v", err)
	}
}

func writeAPIError(w http.ResponseWriter, status int, errType, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	})
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, http.StatusMethodNotAllowed, "invalid_request_error",
		fmt.Sprintf("method %s is not allowed on %s", r.Method, r.URL.Path))
}

func snippet(data []byte) string {
	text := strings.TrimSpace(string(data))
	if len(text) > maxErrorSnippet {
		text = text[:maxErrorSnippet]
	}
	return text
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return value
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", n*2)
	}
	return hex.EncodeToString(buf)
}
