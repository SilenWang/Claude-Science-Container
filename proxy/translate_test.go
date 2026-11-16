package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func testConfig(backendName, baseURL string) *config {
	return &config{
		DefaultBackend:    backendName,
		ForceModel:        "",
		ModelListMode:     "aliases_first",
		InlineImagePolicy: "preserve",
		ProxyHost:         "127.0.0.1",
		ProxyPort:         9876,
		backends: map[string]*backend{
			"deepseek": {name: "deepseek", baseURL: normalizeBaseURL(baseURL), apiKey: "test-key"},
			"openai":   {name: "openai", baseURL: normalizeBaseURL(baseURL), apiKey: "test-key"},
			"custom":   {name: "custom", baseURL: normalizeBaseURL(baseURL), apiKey: "test-key"},
		},
	}
}

func mustRequest(t *testing.T, raw string) *anthropicRequest {
	t.Helper()
	var req anthropicRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("parse request: %v", err)
	}
	return &req
}

func TestSystemPromptStringAndBlocks(t *testing.T) {
	cfg := testConfig("deepseek", "http://upstream")

	req := mustRequest(t, `{
		"model":"claude-sonnet-4-5","max_tokens":32,
		"system":"be brief",
		"messages":[{"role":"user","content":"hi"}]
	}`)
	body, _, err := anthropicToOpenAI(req, "deepseek-chat", cfg, "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]map[string]any)
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "be brief" {
		t.Fatalf("system message = %#v", msgs[0])
	}

	req = mustRequest(t, `{
		"model":"claude-sonnet-4-5","max_tokens":32,
		"system":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}],
		"messages":[{"role":"user","content":"hi"}]
	}`)
	body, _, err = anthropicToOpenAI(req, "deepseek-chat", cfg, "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	msgs = body["messages"].([]map[string]any)
	if got := msgs[0]["content"]; got != "line one\nline two" {
		t.Fatalf("joined system = %q", got)
	}
}

func TestStopSequences(t *testing.T) {
	cfg := testConfig("deepseek", "http://upstream")

	single := mustRequest(t, `{"max_tokens":8,"stop_sequences":["END"],"messages":[{"role":"user","content":"x"}]}`)
	body, _, _ := anthropicToOpenAI(single, "m", cfg, "deepseek")
	if body["stop"] != "END" {
		t.Fatalf("single stop = %#v", body["stop"])
	}

	multi := mustRequest(t, `{"max_tokens":8,"stop_sequences":["A","B"],"messages":[{"role":"user","content":"x"}]}`)
	body, _, _ = anthropicToOpenAI(multi, "m", cfg, "deepseek")
	stops, ok := body["stop"].([]string)
	if !ok || len(stops) != 2 {
		t.Fatalf("multi stop = %#v", body["stop"])
	}
}

func TestToolResultAndToolUse(t *testing.T) {
	cfg := testConfig("deepseek", "http://upstream")
	req := mustRequest(t, `{
		"max_tokens":64,
		"tools":[{"name":"get weather","description":"weather","input_schema":{"properties":{"city":{"type":"string"}}}}],
		"messages":[
			{"role":"assistant","content":[
				{"type":"text","text":"checking"},
				{"type":"tool_use","id":"toolu_1","name":"get weather","input":{"city":"Beijing"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"sunny"}]},
				{"type":"text","text":"thanks"}
			]}
		]
	}`)

	body, tr, err := anthropicToOpenAI(req, "deepseek-chat", cfg, "deepseek")
	if err != nil {
		t.Fatal(err)
	}

	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %#v", msgs)
	}

	assistant := msgs[0]
	calls, ok := assistant["tool_calls"].([]map[string]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %#v", assistant["tool_calls"])
	}
	fn := calls[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("sanitized tool name = %q", fn["name"])
	}
	if tr.toolNames["get_weather"] != "get weather" {
		t.Fatalf("tool name lookup = %#v", tr.toolNames)
	}
	if !strings.Contains(fn["arguments"].(string), "Beijing") {
		t.Fatalf("arguments = %q", fn["arguments"])
	}

	toolMsg := msgs[1]
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "toolu_1" || toolMsg["content"] != "sunny" {
		t.Fatalf("tool message = %#v", toolMsg)
	}
	userMsg := msgs[2]
	if userMsg["role"] != "user" || userMsg["content"] != "thanks" {
		t.Fatalf("user message = %#v", userMsg)
	}

	tools := body["tools"].([]map[string]any)
	params := tools[0]["function"].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("schema type = %#v", params["type"])
	}
}

func TestImageBase64BecomesDataURL(t *testing.T) {
	cfg := testConfig("custom", "http://upstream")
	req := mustRequest(t, `{
		"max_tokens":16,
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},
			{"type":"text","text":"what is this"}
		]}]
	}`)

	body, _, err := anthropicToOpenAI(req, "vision-model", cfg, "custom")
	if err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]map[string]any)
	parts, ok := msgs[0]["content"].([]map[string]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content parts = %#v", msgs[0]["content"])
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "what is this" {
		t.Fatalf("text part = %#v", parts[0])
	}
	imageURL := parts[1]["image_url"].(map[string]any)["url"]
	if imageURL != "data:image/png;base64,AAAA" {
		t.Fatalf("image url = %q", imageURL)
	}
}

func TestImageOmitPolicy(t *testing.T) {
	cfg := testConfig("custom", "http://upstream")
	cfg.InlineImagePolicy = "omit"
	req := mustRequest(t, `{
		"max_tokens":16,
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},
			{"type":"text","text":"look"}
		]}]
	}`)

	body, _, _ := anthropicToOpenAI(req, "vision-model", cfg, "custom")
	msgs := body["messages"].([]map[string]any)
	text, ok := msgs[0]["content"].(string)
	if !ok || !strings.Contains(text, "omitted") {
		t.Fatalf("omitted image content = %#v", msgs[0]["content"])
	}
}

func TestResponseTranslation(t *testing.T) {
	tr := &translation{toolNames: map[string]string{"get_weather": "get weather"}}
	resp := parseOpenAIResponse(t, `{
		"choices":[{
			"message":{
				"content":"hello",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}}]
			},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":12,"completion_tokens":3}
	}`)

	out := openAIToAnthropic(resp, "claude-sonnet-4-5", "msg_test", tr)
	if out["type"] != "message" || out["model"] != "claude-sonnet-4-5" {
		t.Fatalf("envelope = %#v", out)
	}
	if out["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %#v", out["stop_reason"])
	}
	blocks := out["content"].([]map[string]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "hello" {
		t.Fatalf("text block = %#v", blocks[0])
	}
	if blocks[1]["type"] != "tool_use" || blocks[1]["name"] != "get weather" {
		t.Fatalf("tool_use block = %#v", blocks[1])
	}
	input := blocks[1]["input"].(map[string]any)
	if input["city"] != "Beijing" {
		t.Fatalf("tool input = %#v", input)
	}
	usage := out["usage"].(map[string]any)
	if usage["input_tokens"] != 12 || usage["output_tokens"] != 3 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestResponseTranslationPlainText(t *testing.T) {
	// reasoning_content 必须被丢弃，只保留正文。
	resp := parseOpenAIResponse(t, `{
		"choices":[{"message":{"content":"收到","reasoning_content":"internal monologue"},"finish_reason":"stop"}]
	}`)

	out := openAIToAnthropic(resp, "claude-sonnet-4-5", "msg_x", &translation{toolNames: map[string]string{}})
	blocks := out["content"].([]map[string]any)
	if len(blocks) != 1 || blocks[0]["text"] != "收到" {
		t.Fatalf("blocks = %#v", blocks)
	}
	if out["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %#v", out["stop_reason"])
	}
}

func parseOpenAIResponse(t *testing.T, raw string) *openAIResponse {
	t.Helper()
	var resp openAIResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("parse openai response: %v", err)
	}
	return &resp
}

func TestNormalizeToolName(t *testing.T) {
	cases := map[string]string{
		"get weather": "get_weather",
		"a.b/c":       "a_b_c",
		"":            "fallback",
	}
	for in, want := range cases {
		if got := normalizeToolName(in, "fallback"); got != want {
			t.Errorf("normalizeToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com":                     "https://api.deepseek.com/v1",
		"https://api.deepseek.com/":                    "https://api.deepseek.com/v1",
		"https://api.deepseek.com/v1":                  "https://api.deepseek.com/v1",
		"https://api.deepseek.com/v1/chat/completions": "https://api.deepseek.com/v1",
		"": "",
	}
	for in, want := range cases {
		if got := normalizeBaseURL(in); got != want {
			t.Errorf("normalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelListModes(t *testing.T) {
	cfg := testConfig("deepseek", "http://upstream")
	cfg.ModelAliases = []modelAlias{{ID: "claude-haiku-4-5", DisplayName: "DeepSeek V4 Flash", Backend: "deepseek", Model: "deepseek-v4-flash-vision-exp"}}

	cfg.ModelListMode = "aliases"
	models := cfg.modelList()
	if len(models) != 1 || models[0]["id"] != "claude-haiku-4-5" || models[0]["display_name"] != "DeepSeek V4 Flash" {
		t.Fatalf("aliases mode = %#v", models)
	}

	cfg.ModelListMode = "builtin"
	models = cfg.modelList()
	if len(models) != len(builtinModels) {
		t.Fatalf("builtin mode = %#v", models)
	}

	cfg.ModelListMode = "aliases_first"
	models = cfg.modelList()
	if models[0]["id"] != "claude-haiku-4-5" || len(models) != len(builtinModels)+1 {
		t.Fatalf("aliases_first mode = %#v", models)
	}
}
