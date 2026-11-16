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

	// 一条 user 消息里混合 tool_result 与 text：tool 必须先于 text，
	// 并紧跟带 tool_calls 的 assistant（OpenAI 的协议硬要求）。
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
	if parts[0]["type"] != "image_url" {
		t.Fatalf("image part = %#v", parts[0])
	}
	if url := parts[0]["image_url"].(map[string]any)["url"]; url != "data:image/png;base64,AAAA" {
		t.Fatalf("image url = %q", url)
	}
	if parts[1]["type"] != "text" || parts[1]["text"] != "what is this" {
		t.Fatalf("text part = %#v", parts[1])
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
	parts, ok := msgs[0]["content"].([]map[string]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("omitted image content = %#v", msgs[0]["content"])
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "look" {
		t.Fatalf("text part = %#v", parts[0])
	}
	if parts[1]["type"] != "text" || !strings.Contains(parts[1]["text"].(string), "omitted") {
		t.Fatalf("omitted note = %#v", parts[1])
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

	// 默认 mode（空）配了 aliases：明确走 aliases，只显示 alias 项。
	cfg.ModelListMode = ""
	models = cfg.modelList()
	if len(models) != 1 || models[0]["id"] != "claude-haiku-4-5" {
		t.Fatalf("default mode with aliases = %#v", models)
	}

	// 默认 mode（空）没有 aliases：只显示内置清单。
	cfg.ModelAliases = nil
	models = cfg.modelList()
	if len(models) != len(builtinModels) || models[0]["id"] != builtinModels[0]["id"] {
		t.Fatalf("default mode without aliases = %#v", models)
	}

	// aliases 模式配了空 alias 清单：就是空清单，不回退到 builtin/aliases_first。
	cfg.ModelListMode = "aliases"
	if models = cfg.modelList(); len(models) != 0 {
		t.Fatalf("empty aliases mode = %#v", models)
	}
}

func TestImagePolicyThreeModes(t *testing.T) {
	// 同一份请求：文本 + base64 图 + 文本 + URL 图。验证三档策略与原顺序。
	raw := `{
		"max_tokens":16,
		"messages":[{"role":"user","content":[
			{"type":"text","text":"first"},
			{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"JPEGDATA"}},
			{"type":"text","text":"second"},
			{"type":"image","source":{"type":"url","url":"https://example.com/cat.png"}}
		]}]
	}`

	tests := []struct {
		policy string
		want   []string // 每项：text:<文本> / image:<url> / note
	}{
		{"preserve", []string{"text:first", "image:data:image/jpeg;base64,JPEGDATA", "text:second", "image:https://example.com/cat.png"}},
		{"omit_inline", []string{"text:first", "text:second", "image:https://example.com/cat.png", "note"}},
		{"omit", []string{"text:first", "text:second", "note"}},
	}

	for _, tc := range tests {
		t.Run(tc.policy, func(t *testing.T) {
			cfg := testConfig("custom", "http://upstream")
			cfg.InlineImagePolicy = tc.policy
			body, _, err := anthropicToOpenAI(mustRequest(t, raw), "vision-model", cfg, "custom")
			if err != nil {
				t.Fatal(err)
			}
			msgs := body["messages"].([]map[string]any)
			parts, ok := msgs[0]["content"].([]map[string]any)
			if !ok {
				t.Fatalf("content parts = %#v", msgs[0]["content"])
			}
			if len(parts) != len(tc.want) {
				t.Fatalf("%s parts = %#v, want %v", tc.policy, parts, tc.want)
			}
			for i, want := range tc.want {
				part := parts[i]
				switch {
				case want == "note":
					text, _ := part["text"].(string)
					if part["type"] != "text" || !strings.Contains(text, "omitted") {
						t.Fatalf("%s part %d = %#v", tc.policy, i, part)
					}
				case strings.HasPrefix(want, "text:"):
					if part["type"] != "text" || part["text"] != strings.TrimPrefix(want, "text:") {
						t.Fatalf("%s part %d = %#v, want %s", tc.policy, i, part, want)
					}
				case strings.HasPrefix(want, "image:"):
					if part["type"] != "image_url" || part["image_url"].(map[string]any)["url"] != strings.TrimPrefix(want, "image:") {
						t.Fatalf("%s part %d = %#v, want %s", tc.policy, i, part, want)
					}
				}
			}
		})
	}
}

// 图片全被剔除、消息里没有正文时，不能给上游发空 user 消息（OpenAI 会 400）：
// 固定为补一条占位文本。
func TestImageOnlyMessageGetsPlaceholderWhenOmitted(t *testing.T) {
	cfg := testConfig("deepseek", "http://upstream")
	cfg.InlineImagePolicy = "omit"
	req := mustRequest(t, `{
		"max_tokens":16,
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
		]}]
	}`)

	body, _, err := anthropicToOpenAI(req, "deepseek-chat", cfg, "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatalf("messages = %#v", msgs)
	}
	text, ok := msgs[0]["content"].(string)
	if !ok || !strings.Contains(text, "omitted") {
		t.Fatalf("placeholder content = %#v", msgs[0]["content"])
	}
}

func TestImageSourceURLPassesThrough(t *testing.T) {
	cfg := testConfig("custom", "http://upstream")
	req := mustRequest(t, `{
		"max_tokens":16,
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"url","url":"https://example.com/a.webp"}}
		]}]
	}`)
	body, _, err := anthropicToOpenAI(req, "vision-model", cfg, "custom")
	if err != nil {
		t.Fatal(err)
	}
	parts := body["messages"].([]map[string]any)[0]["content"].([]map[string]any)
	if len(parts) != 1 || parts[0]["image_url"].(map[string]any)["url"] != "https://example.com/a.webp" {
		t.Fatalf("parts = %#v", parts)
	}
}

// 同一个 user 消息里正文与 tool_result 混排：tool 消息必须紧跟在带 tool_calls 的
// assistant 之后，正文另起一条 user（DeepSeek / vLLM 会校验这个顺序）。
func TestToolResultPrecedesUserText(t *testing.T) {
	cfg := testConfig("deepseek", "http://upstream")
	req := mustRequest(t, `{
		"max_tokens":64,
		"tools":[{"name":"run code","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"user","content":"跑一下"},
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"run code","input":{"code":"print(1)"}}]},
			{"role":"user","content":[
				{"type":"text","text":"结果怎么样"},
				{"type":"tool_result","tool_use_id":"call_1","content":"1"}
			]}
		]
	}`)

	body, _, err := anthropicToOpenAI(req, "deepseek-chat", cfg, "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 4 {
		t.Fatalf("messages = %#v", msgs)
	}
	if _, ok := msgs[1]["tool_calls"]; !ok {
		t.Fatalf("assistant tool_calls = %#v", msgs[1])
	}
	if msgs[2]["role"] != "tool" || msgs[2]["tool_call_id"] != "call_1" {
		t.Fatalf("tool message must immediately follow assistant: %#v", msgs[2])
	}
	if msgs[3]["role"] != "user" || msgs[3]["content"] != "结果怎么样" {
		t.Fatalf("user message = %#v", msgs[3])
	}
}
