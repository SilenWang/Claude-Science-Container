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

const toolRequestPrefix = `{
	"model":"claude-sonnet-4-5","max_tokens":256,
	"tools":[
		{"name":"get weather","description":"查城市天气","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},
		{"name":"get time","description":"查城市时间","input_schema":{"type":"object","properties":{"tz":{"type":"string"}}}}
	],
	"messages":[{"role":"user","content":"北京天气怎么样？"}]
`

// contentBlocks 把响应里的 content 转成通用 map，方便按类型取块。
// 同时接受内存里的 []map[string]any 与 JSON 反序列化后的 []any。
func contentBlocks(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	switch v := out["content"].(type) {
	case []map[string]any:
		return v
	case []any:
		blocks := make([]map[string]any, 0, len(v))
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("content item is not an object: %#v", item)
			}
			blocks = append(blocks, block)
		}
		return blocks
	default:
		t.Fatalf("content is not a block list: %#v", out["content"])
		return nil
	}
}

func toolUseBlocks(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	var tools []map[string]any
	for _, block := range contentBlocks(t, out) {
		if block["type"] == "tool_use" {
			tools = append(tools, block)
		}
	}
	return tools
}

// ---- 非流式 ----

func TestNonStreamSingleToolCall(t *testing.T) {
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&upstreamBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"choices":[{
				"message":{"content":"","tool_calls":[
					{"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}
				]},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":31,"completion_tokens":9}
		}`)
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 请求侧：Anthropic tools.input_schema → OpenAI function.parameters。
	tools, _ := upstreamBody["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("upstream tools = %#v", upstreamBody["tools"])
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("sanitized tool name = %#v", fn["name"])
	}
	params := fn["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("parameters = %#v", params)
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not json: %v", err)
	}
	if out["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %#v", out["stop_reason"])
	}
	blocks := contentBlocks(t, out)
	if len(blocks) != 1 || blocks[0]["type"] != "tool_use" {
		t.Fatalf("blocks = %#v", blocks)
	}
	if blocks[0]["id"] != "call_abc" {
		t.Fatalf("tool_use id must pass through upstream id, got %#v", blocks[0]["id"])
	}
	if blocks[0]["name"] != "get weather" {
		t.Fatalf("tool_use name must be restored, got %#v", blocks[0]["name"])
	}
	input := blocks[0]["input"].(map[string]any)
	if input["city"] != "北京" {
		t.Fatalf("tool input = %#v", input)
	}
	usage := out["usage"].(map[string]any)
	if usage["input_tokens"] != float64(31) || usage["output_tokens"] != float64(9) {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestNonStreamParallelToolCalls(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"choices":[{
				"message":{
					"content":"两个都查",
					"tool_calls":[
						{"id":"call_a","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}},
						{"id":"call_b","type":"function","function":{"name":"get_time","arguments":"{\"tz\":\"Asia/Shanghai\"}"}}
					]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":20,"completion_tokens":11}
		}`)
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not json: %v", err)
	}
	if out["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %#v", out["stop_reason"])
	}
	blocks := contentBlocks(t, out)
	// text 在前，tool_use 按上游顺序在后。
	if len(blocks) != 3 {
		t.Fatalf("blocks = %#v", blocks)
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "两个都查" {
		t.Fatalf("text block = %#v", blocks[0])
	}
	wantNames := []string{"get weather", "get time"}
	wantIDs := []string{"call_a", "call_b"}
	for i, block := range blocks[1:] {
		if block["type"] != "tool_use" {
			t.Fatalf("block %d = %#v", i+1, block)
		}
		if block["name"] != wantNames[i] {
			t.Fatalf("block %d name = %#v, want %q", i+1, block["name"], wantNames[i])
		}
		if block["id"] != wantIDs[i] {
			t.Fatalf("block %d id = %#v, want %q", i+1, block["id"], wantIDs[i])
		}
	}
}

// 两个不同原名 sanitize 后撞车：必须各自回填，不能互相串错。
func TestToolNameCollisionRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"choices":[{
				"message":{"tool_calls":[
					{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}},
					{"id":"call_2","type":"function","function":{"name":"get_weather_2","arguments":"{}"}}
				]},
				"finish_reason":"tool_calls"
			}]
		}`)
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", `{
		"model":"claude-sonnet-4-5","max_tokens":128,
		"tools":[
			{"name":"get weather","input_schema":{"type":"object"}},
			{"name":"get/weather","input_schema":{"type":"object"}}
		],
		"messages":[{"role":"user","content":"两个都调"}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not json: %v", err)
	}
	tools := toolUseBlocks(t, out)
	if len(tools) != 2 {
		t.Fatalf("tool_use blocks = %#v", tools)
	}
	if tools[0]["id"] != "call_1" || tools[0]["name"] != "get weather" {
		t.Fatalf("first tool = %#v", tools[0])
	}
	if tools[1]["id"] != "call_2" || tools[1]["name"] != "get/weather" {
		t.Fatalf("second tool = %#v", tools[1])
	}
}

// ---- 流式 ----

// streamFromFrames 把帧列表写进响应，按 cutAt 把某帧从中间切开，覆盖「一个 JSON 帧跨 chunk」。
func streamFromFrames(t *testing.T, frames []string, cutFrame int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i, frame := range frames {
			if i != cutFrame {
				_, _ = io.WriteString(w, frame)
				flushIfPossible(w)
				continue
			}
			cut := strings.Index(frame, "arguments")
			if cut < 0 {
				t.Errorf("frame %d has no split point: %q", i, frame)
				return
			}
			_, _ = io.WriteString(w, frame[:cut])
			flushIfPossible(w)
			time.Sleep(2 * time.Millisecond)
			_, _ = io.WriteString(w, frame[cut:])
			flushIfPossible(w)
		}
	}
}

func sseData(payload string) string { return "data: " + payload + "\n\n" }

func TestStreamToolCallIsIncremental(t *testing.T) {
	// 一个 JSON 帧（第 2 条 arguments 片段）会被 TCP 切成两块写入。
	frames := []string{
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`),
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`),
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"北京\""}}]}}]}`),
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":",\"unit\":\"c\"}"}}]}}]}`),
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":7}}`),
		"data: [DONE]\n\n",
	}

	upstream := httptest.NewServer(streamFromFrames(t, frames, 2))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`,"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	all := parseSSE(t, rec.Body.String())
	var names []string
	for _, f := range all {
		names = append(names, f.name)
	}
	want := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("frames = %v, want %v", names, want)
	}

	start := all[1].payload
	if start["index"] != float64(0) {
		t.Fatalf("tool_use index = %#v", start["index"])
	}
	block := start["content_block"].(map[string]any)
	if block["type"] != "tool_use" || block["id"] != "call_abc" || block["name"] != "get weather" {
		t.Fatalf("content_block = %#v", block)
	}
	if input, ok := block["input"].(map[string]any); !ok || len(input) != 0 {
		t.Fatalf("content_block.input must start empty, got %#v", block["input"])
	}

	// 逐段下发：三个 delta，且是上游 arguments 的原样片段。
	var partial strings.Builder
	for _, f := range all[2:5] {
		if f.payload["index"] != float64(0) {
			t.Fatalf("delta index = %#v", f.payload["index"])
		}
		delta := f.payload["delta"].(map[string]any)
		if delta["type"] != "input_json_delta" {
			t.Fatalf("delta type = %#v", delta["type"])
		}
		partial.WriteString(delta["partial_json"].(string))
	}
	if got := partial.String(); got != `{"city":"北京","unit":"c"}` {
		t.Fatalf("partial_json concat = %q", got)
	}

	msgDelta := all[6].payload["delta"].(map[string]any)
	if msgDelta["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %#v", msgDelta["stop_reason"])
	}
	usage := all[6].payload["usage"].(map[string]any)
	if usage["output_tokens"] != float64(7) {
		t.Fatalf("output_tokens = %#v", usage["output_tokens"])
	}
}

func TestStreamParallelToolCalls(t *testing.T) {
	frames := []string{
		// 第一帧同时声明两个工具（并行调用的常见形态）。
		sseData(`{"choices":[{"delta":{"tool_calls":[` +
			`{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}},` +
			`{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":""}}]}}]}`),
		// 之后两路的 arguments 交错到达。
		sseData(`{"choices":[{"delta":{"tool_calls":[` +
			`{"index":0,"function":{"arguments":"{\"city\":"}},` +
			`{"index":1,"function":{"arguments":"{\"tz\":"}}]}}]}`),
		sseData(`{"choices":[{"delta":{"tool_calls":[` +
			`{"index":0,"function":{"arguments":"\"北京\"}"}},` +
			`{"index":1,"function":{"arguments":"\"UTC\"}"}}]}}]}`),
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	}

	upstream := httptest.NewServer(streamFromFrames(t, frames, -1))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`,"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	all := parseSSE(t, rec.Body.String())

	type blockInfo struct {
		id, name string
		index    int
		partial  strings.Builder
		started  bool
		stopped  bool
	}
	blocks := map[int]*blockInfo{}

	for _, f := range all {
		switch f.name {
		case "content_block_start":
			index := int(f.payload["index"].(float64))
			block := f.payload["content_block"].(map[string]any)
			if block["type"] != "tool_use" {
				t.Fatalf("block %d is not tool_use: %#v", index, block)
			}
			blocks[index] = &blockInfo{
				id:      block["id"].(string),
				name:    block["name"].(string),
				index:   index,
				started: true,
			}
		case "content_block_delta":
			index := int(f.payload["index"].(float64))
			delta := f.payload["delta"].(map[string]any)
			if delta["type"] != "input_json_delta" {
				t.Fatalf("delta on block %d = %#v", index, delta)
			}
			info, ok := blocks[index]
			if !ok {
				t.Fatalf("delta before content_block_start on %d", index)
			}
			info.partial.WriteString(delta["partial_json"].(string))
		case "content_block_stop":
			index := int(f.payload["index"].(float64))
			if info, ok := blocks[index]; ok {
				info.stopped = true
			}
		}
	}

	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v", blocks)
	}
	// 无文本：并行工具按上游 index 升序拿 0、1。
	first, ok := blocks[0]
	if !ok {
		t.Fatalf("missing block 0: %#v", blocks)
	}
	second, ok := blocks[1]
	if !ok {
		t.Fatalf("missing block 1: %#v", blocks)
	}
	if first.id != "call_a" || first.name != "get weather" || !first.stopped {
		t.Fatalf("block 0 = %#v", first)
	}
	if second.id != "call_b" || second.name != "get time" || !second.stopped {
		t.Fatalf("block 1 = %#v", second)
	}
	if first.partial.String() != `{"city":"北京"}` {
		t.Fatalf("block 0 partial_json = %q", first.partial.String())
	}
	if second.partial.String() != `{"tz":"UTC"}` {
		t.Fatalf("block 1 partial_json = %q", second.partial.String())
	}

	last := all[len(all)-2].payload
	delta := last["delta"].(map[string]any)
	if delta["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %#v", delta["stop_reason"])
	}
}

// 上游把 id/name 放到后面的 delta 才给：不能丢参数，也不能错开块。
func TestStreamToolCallLateName(t *testing.T) {
	frames := []string{
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"n\":"}}]}}]}`),
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_late","function":{"name":"get_weather"}}]}}]}`),
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`),
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	}

	upstream := httptest.NewServer(streamFromFrames(t, frames, -1))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`,"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	all := parseSSE(t, rec.Body.String())
	var startBlock map[string]any
	var partial strings.Builder
	for _, f := range all {
		switch f.name {
		case "content_block_start":
			startBlock = f.payload["content_block"].(map[string]any)
		case "content_block_delta":
			delta := f.payload["delta"].(map[string]any)
			partial.WriteString(delta["partial_json"].(string))
		}
	}
	if startBlock == nil {
		t.Fatalf("no content_block_start: %v", frameNames(all))
	}
	if startBlock["id"] != "call_late" || startBlock["name"] != "get weather" {
		t.Fatalf("content_block = %#v", startBlock)
	}
	if partial.String() != `{"n":1}` {
		t.Fatalf("partial_json = %q", partial.String())
	}
}

// 文本 + 工具：文本块先占 index 0 并关闭，工具块拿 index 1。
func TestStreamTextThenToolIndices(t *testing.T) {
	frames := []string{
		sseData(`{"choices":[{"delta":{"content":"我先查一下。"}}]}`),
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]}}]}`),
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	}

	upstream := httptest.NewServer(streamFromFrames(t, frames, -1))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`,"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	all := parseSSE(t, rec.Body.String())
	var textIndex, textStopAt, toolStartIndex, toolStartAt = -1, -1, -1, -1
	for i, f := range all {
		switch f.name {
		case "content_block_start":
			block := f.payload["content_block"].(map[string]any)
			if block["type"] == "text" {
				textIndex = int(f.payload["index"].(float64))
			} else {
				toolStartIndex = int(f.payload["index"].(float64))
				toolStartAt = i
			}
		case "content_block_stop":
			if int(f.payload["index"].(float64)) == textIndex && textIndex >= 0 {
				textStopAt = i
			}
		}
	}
	if textIndex != 0 {
		t.Fatalf("text index = %d", textIndex)
	}
	if toolStartIndex != 1 {
		t.Fatalf("tool index = %d, want 1", toolStartIndex)
	}
	if textStopAt < 0 || textStopAt > toolStartAt {
		t.Fatalf("text block must stop before tool block starts (stopAt=%d toolStartAt=%d)", textStopAt, toolStartAt)
	}
}

// ---- 历史 tool_result 往返 ----

func TestToolResultHistoryRoundTrip(t *testing.T) {
	var bodies []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			// 第一轮：上游要求调用工具。
			_, _ = io.WriteString(w, `{
				"choices":[{"message":{"tool_calls":[
					{"id":"call_x","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}
				]},"finish_reason":"tool_calls"}]
			}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"choices":[{"message":{"content":"北京晴，25 度"},"finish_reason":"stop"}]
		}`)
	}))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)

	// 第一轮：拿到 tool_use。
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("first body is not json: %v", err)
	}
	tools := toolUseBlocks(t, out)
	if len(tools) != 1 || tools[0]["id"] != "call_x" || tools[0]["name"] != "get weather" {
		t.Fatalf("first tool_use = %#v", tools)
	}

	// 第二轮：把 tool_use / tool_result 原样带回（CS 的真实往返形态）。
	rec = doRequest(t, srv, http.MethodPost, "/v1/messages", `{
		"model":"claude-sonnet-4-5","max_tokens":256,
		"tools":[
			{"name":"get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}},
			{"name":"get time","input_schema":{"type":"object","properties":{"tz":{"type":"string"}}}}
		],
		"messages":[
			{"role":"user","content":"北京天气怎么样？"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_x","name":"get weather","input":{"city":"北京"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_x","content":[{"type":"text","text":"晴，25 度"}]},
				{"type":"text","text":"谢谢"}
			]}
		]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(bodies) != 2 {
		t.Fatalf("upstream calls = %d", len(bodies))
	}

	msgs, _ := bodies[1]["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("upstream messages = %#v", bodies[1]["messages"])
	}

	// assistant 的 tool_use → tool_calls，name 仍然用同一个安全名，id 原样。
	assistant := msgs[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("assistant tool_calls = %#v", assistant["tool_calls"])
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call_x" {
		t.Fatalf("tool_call id = %#v", call["id"])
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("tool_call name = %#v", fn["name"])
	}
	if args, _ := fn["arguments"].(string); !strings.Contains(args, "北京") {
		t.Fatalf("tool_call arguments = %#v", fn["arguments"])
	}

	// user 消息里混合 tool_result 与 text：先 tool，再 user，且 tool_call_id 对得上。
	toolMsg := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_x" {
		t.Fatalf("tool message = %#v", toolMsg)
	}
	if toolMsg["content"] != "晴，25 度" {
		t.Fatalf("tool content = %#v", toolMsg["content"])
	}
	userMsg := msgs[3].(map[string]any)
	if userMsg["role"] != "user" || userMsg["content"] != "谢谢" {
		t.Fatalf("user message = %#v", userMsg)
	}

	var reply map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("second body is not json: %v", err)
	}
	if reply["stop_reason"] != "end_turn" {
		t.Fatalf("second stop_reason = %#v", reply["stop_reason"])
	}
}

// 只有 tool_result、没有正文的 user 消息：不要凭空造一条空 user 消息。
func TestToolResultWithoutTextStaysToolOnly(t *testing.T) {
	cfg := testConfig("deepseek", "http://upstream")
	req := mustRequest(t, `{
		"max_tokens":64,
		"tools":[{"name":"run code","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"run code","input":{"code":"print(1)"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"1"}]}
		]
	}`)

	body, tr, err := anthropicToOpenAI(req, "deepseek-chat", cfg, "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]map[string]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v", msgs)
	}
	if msgs[1]["role"] != "tool" || msgs[1]["tool_call_id"] != "call_1" {
		t.Fatalf("tool message = %#v", msgs[1])
	}
	if tr.toolNames["run_code"] != "run code" {
		t.Fatalf("tool name mapping = %#v", tr.toolNames)
	}
}

// 上游把 name/id 拆成非前缀片段（"get_wea" + "ther"）：必须按 append 拼完整，
// 不能截断成半截名字。
func TestStreamToolCallSplitNameAppended(t *testing.T) {
	frames := []string{
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_","function":{"name":"get_wea","arguments":""}}]}}]}`),
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"ther","arguments":"{\"city\":\"北京\"}"}}]}}]}`),
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	}

	upstream := httptest.NewServer(streamFromFrames(t, frames, -1))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`,"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	all := parseSSE(t, rec.Body.String())
	var block map[string]any
	var partial strings.Builder
	for _, f := range all {
		switch f.name {
		case "content_block_start":
			block = f.payload["content_block"].(map[string]any)
		case "content_block_delta":
			delta := f.payload["delta"].(map[string]any)
			partial.WriteString(delta["partial_json"].(string))
		}
	}
	if block == nil {
		t.Fatalf("no content_block_start: %v", frameNames(all))
	}
	if block["id"] != "call_1" || block["name"] != "get weather" {
		t.Fatalf("content_block = %#v", block)
	}
	if partial.String() != `{"city":"北京"}` {
		t.Fatalf("partial_json = %q", partial.String())
	}
}

// 上游 tool_calls[].index 乱序，且低 index 的块一直没凑齐 name/id：finish 阶段
// 必须补开该块（取下一个 block index），参数不能静默丢失。
func TestStreamOutOfOrderToolCallStartedAtFinish(t *testing.T) {
	frames := []string{
		// index 1 先到且完整，立即打开 block 0。
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"get_time","arguments":"{\"tz\":\"UTC\"}"}}]}}]}`),
		// index 0 后到，只有参数，没有 name/id：无法立即开块。
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"北京\"}"}}]}}]}`),
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	}

	upstream := httptest.NewServer(streamFromFrames(t, frames, -1))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`,"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	all := parseSSE(t, rec.Body.String())
	var starts []map[string]any
	partials := map[int]string{}
	stops := map[int]bool{}
	for _, f := range all {
		switch f.name {
		case "content_block_start":
			block := f.payload["content_block"].(map[string]any)
			if block["type"] != "tool_use" {
				t.Fatalf("block is not tool_use: %#v", block)
			}
			starts = append(starts, map[string]any{
				"index": int(f.payload["index"].(float64)),
				"block": block,
			})
		case "content_block_delta":
			index := int(f.payload["index"].(float64))
			delta := f.payload["delta"].(map[string]any)
			partials[index] += delta["partial_json"].(string)
		case "content_block_stop":
			stops[int(f.payload["index"].(float64))] = true
		}
	}
	if len(starts) != 2 {
		t.Fatalf("tool blocks = %#v (frames = %v)", starts, frameNames(all))
	}
	// 已打开的块按事件顺序拿 index 0、1；后补开的块不能复用 index。
	if starts[0]["index"] != 0 || starts[1]["index"] != 1 {
		t.Fatalf("block indices = %#v", starts)
	}
	first := starts[0]["block"].(map[string]any)
	if first["id"] != "call_b" || first["name"] != "get time" {
		t.Fatalf("first block = %#v", first)
	}
	second := starts[1]["block"].(map[string]any)
	if second["type"] != "tool_use" {
		t.Fatalf("second block = %#v", second)
	}
	if !strings.Contains(partials[1], "北京") {
		t.Fatalf("out-of-order arguments lost: partials = %#v", partials)
	}
	if !stops[0] || !stops[1] {
		t.Fatalf("blocks not stopped: %#v", stops)
	}
}

// tool_calls 之后的尾随文本继续丢弃（已知问题，行为固定在这里）。
func TestStreamTextAfterToolCallIsDropped(t *testing.T) {
	frames := []string{
		sseData(`{"choices":[{"delta":{"content":"先查一下"}}]}`),
		sseData(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]}}]}`),
		sseData(`{"choices":[{"delta":{"content":"这后面的话不该出现"}}]}`),
		sseData(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	}

	upstream := httptest.NewServer(streamFromFrames(t, frames, -1))
	defer upstream.Close()

	srv, _ := newTestServer(t, upstream.URL)
	rec := doRequest(t, srv, http.MethodPost, "/v1/messages", toolRequestPrefix+`,"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	all := parseSSE(t, rec.Body.String())
	var text strings.Builder
	for _, f := range all {
		if f.name != "content_block_delta" {
			continue
		}
		delta := f.payload["delta"].(map[string]any)
		if delta["type"] == "text_delta" {
			text.WriteString(delta["text"].(string))
		}
	}
	if got := text.String(); got != "先查一下" {
		t.Fatalf("streamed text = %q, trailing text must be dropped", got)
	}
}
