package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Anthropic Messages API 请求的最小可用结构。M1 只覆盖文本、基础工具映射与图片。
type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        json.RawMessage    `json:"system"`
	Messages      []anthropicMessage `json:"messages"`
	Temperature   *float64           `json:"temperature"`
	TopP          *float64           `json:"top_p"`
	StopSequences []string           `json:"stop_sequences"`
	Stream        bool               `json:"stream"`
	Tools         []anthropicTool    `json:"tools"`
	ToolChoice    json.RawMessage    `json:"tool_choice"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicBlock struct {
	Type      string                `json:"type"`
	Text      string                `json:"text"`
	ToolUseID string                `json:"tool_use_id"`
	Content   json.RawMessage       `json:"content"`
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Input     json.RawMessage       `json:"input"`
	Source    *anthropicImageSource `json:"source"`
	ImageURL  json.RawMessage       `json:"image_url"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	URL       string `json:"url"`
}

// translation 携带一次请求翻译的可复用映射。
type translation struct {
	// toolNames 是 OpenAI 安全名 -> Anthropic 原名，用于把响应的工具名回填。
	toolNames map[string]string
	// toolOriginals 是 Anthropic 原名 -> OpenAI 安全名，保证同一个原名每次登记都得到同一个安全名。
	toolOriginals map[string]string
}

var toolNameRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// maxToolNameLen 是 OpenAI 对 function name 的长度上限。
const maxToolNameLen = 64

func newTranslation() *translation {
	return &translation{
		toolNames:     map[string]string{},
		toolOriginals: map[string]string{},
	}
}

// registerToolName 把 Anthropic 工具名换成 OpenAI 安全名并登记双向映射，返回安全名。
//   - 同一个原名重复登记返回同一个安全名（历史 assistant 的 tool_use 与 tools 列表能对上）。
//   - 两个不同原名 sanitize 后撞车时，后来的追加 _2/_3 后缀，保证上游回来的名字
//     能一一回填到各自原名，不会互相串错。
func (tr *translation) registerToolName(original, fallback string) string {
	if tr.toolNames == nil {
		tr.toolNames = map[string]string{}
	}
	if tr.toolOriginals == nil {
		tr.toolOriginals = map[string]string{}
	}
	if original == "" {
		original = fallback
	}
	if safe, ok := tr.toolOriginals[original]; ok && safe != "" {
		return safe
	}
	base := normalizeToolName(original, fallback)
	safe := base
	for n := 2; ; n++ {
		existing, taken := tr.toolNames[safe]
		if !taken || existing == original {
			break
		}
		safe = suffixToolName(base, n)
	}
	tr.toolNames[safe] = original
	tr.toolOriginals[original] = safe
	return safe
}

// originalToolName 把上游返回的安全名还原成 Anthropic 原名；未登记的原样返回。
func (tr *translation) originalToolName(safe string) string {
	if original, ok := tr.toolNames[safe]; ok && original != "" {
		return original
	}
	return safe
}

func suffixToolName(base string, n int) string {
	suffix := "_" + strconv.Itoa(n)
	if len(base)+len(suffix) > maxToolNameLen {
		base = base[:maxToolNameLen-len(suffix)]
	}
	return base + suffix
}

// anthropicToOpenAI 把 Anthropic Messages 请求翻译成 OpenAI Chat Completions 请求。
// stream 由 req.Stream 决定：M2 起流式请求会把 stream=true 透传给上游。
func anthropicToOpenAI(req *anthropicRequest, backendModel string, cfg *config, backendName string) (map[string]any, *translation, error) {
	messages := make([]map[string]any, 0, len(req.Messages)+1)
	tr := newTranslation()

	// 工具先登记：tools 里声明过的名字优先占用安全名，历史 assistant 的 tool_use
	// 随后复用同一份映射，避免同一次请求里两处得到不同的名字。
	tools := translateTools(req.Tools, tr)

	if system := systemText(req.System); system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}

	imagePolicy := imagePolicyFor(backendName, cfg.InlineImagePolicy)

	for _, msg := range req.Messages {
		switch msg.Role {
		case "assistant":
			messages = append(messages, translateAssistantMessage(msg, tr))
		default:
			messages = append(messages, translateUserMessage(msg, imagePolicy)...)
		}
	}

	body := map[string]any{
		"model":    backendModel,
		"messages": messages,
	}
	if req.Stream {
		body["stream"] = true
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body["max_tokens"] = maxTokens
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopSequences) == 1 {
		body["stop"] = req.StopSequences[0]
	} else if len(req.StopSequences) > 1 {
		body["stop"] = req.StopSequences
	}

	if len(tools) > 0 {
		body["tools"] = tools
		if choice := convertToolChoice(req.ToolChoice, tr, backendName); choice != nil {
			body["tool_choice"] = choice
		}
	}

	return body, tr, nil
}

func systemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []anthropicBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// decodeBlocks 把 content 解析成文本或 block 列表。
func decodeBlocks(raw json.RawMessage) (text string, blocks []anthropicBlock, isText bool) {
	if len(raw) == 0 {
		return "", nil, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil, true
	}
	_ = json.Unmarshal(raw, &blocks)
	return "", blocks, false
}

// translateUserMessage 把一条 Anthropic user 消息翻译成 OpenAI 消息序列。
// 顺序固定为「先 tool 后 user」：OpenAI / DeepSeek / vLLM 都要求 role:"tool"
// 紧随带 tool_calls 的 assistant；同一 Anthropic 消息里的正文另起一条 user。
// text 与 image 块保持它们在 Anthropic content 数组里的原顺序。
func translateUserMessage(msg anthropicMessage, imagePolicy string) []map[string]any {
	text, blocks, isText := decodeBlocks(msg.Content)
	if isText {
		if text == "" {
			return nil
		}
		return []map[string]any{{"role": "user", "content": text}}
	}

	var toolMessages []map[string]any
	parts := make([]map[string]any, 0, len(blocks))
	omitted := 0

	for _, block := range blocks {
		switch block.Type {
		case "tool_result":
			toolMessages = append(toolMessages, map[string]any{
				"role":         "tool",
				"tool_call_id": block.ToolUseID,
				"content":      toolResultText(block.Content),
			})
		case "text":
			if block.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": block.Text})
			}
		case "image", "image_url":
			url := openAIImageURL(block)
			if url == "" || imageOmitted(url, imagePolicy) {
				omitted++
				continue
			}
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
			})
		}
	}

	// 图片全被剔除后不能把空的 user 消息发给上游（OpenAI 会 400）：
	// 补一条占位文本。只丢了一部分时同样追加，提示模型这里有图但未转发。
	if omitted > 0 {
		parts = append(parts, map[string]any{
			"type": "text",
			"text": fmt.Sprintf("[%d image attachment(s) omitted by INLINE_IMAGE_POLICY=%s.]", omitted, imagePolicy),
		})
	}

	out := make([]map[string]any, 0, len(toolMessages)+1)
	out = append(out, toolMessages...)
	if content := openAIContentParts(parts); content != nil {
		out = append(out, map[string]any{"role": "user", "content": content})
	}
	return out
}

// openAIContentParts 在只有一个 text 块时沿用字符串 content（M1/M2 的既有形态），
// 其余情况（多段或含图片）用 OpenAI 的 part 数组。
func openAIContentParts(parts []map[string]any) any {
	switch len(parts) {
	case 0:
		return nil
	case 1:
		if parts[0]["type"] == "text" {
			return parts[0]["text"]
		}
	}
	return parts
}

// imageOmitted 实现 INLINE_IMAGE_POLICY 的三档语义：
//   - preserve：全部保留
//   - omit：丢弃所有图片块（base64 内联图与 http(s) URL 图都丢）
//   - omit_inline：只丢 base64 data: 内联图，保留 http(s) URL 图（默认）
func imageOmitted(url, policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "omit":
		return true
	case "omit_inline":
		return strings.HasPrefix(url, "data:")
	}
	return false
}

func translateAssistantMessage(msg anthropicMessage, tr *translation) map[string]any {
	text, blocks, isText := decodeBlocks(msg.Content)
	if isText {
		return map[string]any{"role": "assistant", "content": text}
	}

	var textParts []string
	var toolCalls []map[string]any
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			safe := tr.registerToolName(block.Name, "tool_0")
			args := "{}"
			if len(block.Input) > 0 {
				if b, err := json.Marshal(block.Input); err == nil {
					args = string(b)
				}
			}
			id := block.ID
			if id == "" {
				id = "toolu_" + randomHex(6)
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      safe,
					"arguments": args,
				},
			})
		}
	}

	out := map[string]any{"role": "assistant"}
	if len(textParts) > 0 {
		out["content"] = strings.Join(textParts, " ")
	} else {
		out["content"] = nil
	}
	if len(toolCalls) > 0 {
		out["tool_calls"] = toolCalls
	}
	return out
}

func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var items []any
	if err := json.Unmarshal(raw, &items); err == nil {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			switch v := item.(type) {
			case string:
				parts = append(parts, v)
			case map[string]any:
				if t, ok := v["text"].(string); ok && v["type"] == "text" {
					parts = append(parts, t)
					continue
				}
				if b, err := json.Marshal(v); err == nil {
					parts = append(parts, string(b))
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	if b, err := json.Marshal(json.RawMessage(raw)); err == nil {
		return string(b)
	}
	return ""
}

func openAIImageURL(block anthropicBlock) string {
	if len(block.ImageURL) > 0 {
		var s string
		if err := json.Unmarshal(block.ImageURL, &s); err == nil && s != "" {
			return s
		}
		var obj struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(block.ImageURL, &obj); err == nil && obj.URL != "" {
			return obj.URL
		}
	}
	source := block.Source
	if source == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(source.Type)) {
	case "url":
		// source.type == "url"：直接透传 url，不做 base64 转换。
		return strings.TrimSpace(source.URL)
	case "base64":
		if source.Data == "" {
			return ""
		}
		return imageDataURL(source.MediaType, source.Data)
	}
	// 未标注 source.type 的容错：有 url 就透传，否则按 base64 处理。
	if source.URL != "" {
		return strings.TrimSpace(source.URL)
	}
	if source.Data == "" {
		return ""
	}
	return imageDataURL(source.MediaType, source.Data)
}

// imageDataURL 用来源的 media_type 拼 data: 前缀，不再一律 image/png。
func imageDataURL(mediaType, data string) string {
	mediaType = strings.TrimSpace(mediaType)
	if mediaType == "" {
		mediaType = "image/png"
	}
	return "data:" + mediaType + ";base64," + data
}

func imagePolicyFor(backendName, configured string) string {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "preserve", "omit", "omit_inline":
		return strings.ToLower(strings.TrimSpace(configured))
	}
	// auto：按后端推断——DeepSeek 官方接口的历史行为是不接受图片，其余上游按原样转发。
	// 这不是默认值：默认是 omit_inline，因为默认模型 deepseek-flash 是纯文本模型，
	// 与后端无关都该丢内联图。auto 留给显式选择了视觉后端的用户。
	if backendName == "deepseek" {
		return "omit"
	}
	return "preserve"
}

func translateTools(tools []anthropicTool, tr *translation) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(tools))
	for idx, tool := range tools {
		fallback := fmt.Sprintf("tool_%d", idx)
		safe := tr.registerToolName(tool.Name, fallback)
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        safe,
				"description": tool.Description,
				"parameters":  sanitizeToolSchema(tool.InputSchema),
			},
		})
	}
	return out
}

func sanitizeToolSchema(raw json.RawMessage) map[string]any {
	schema := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &schema); err != nil {
			schema = map[string]any{}
		}
	}
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}
	if schema["type"] == "object" {
		if _, ok := schema["properties"]; !ok {
			schema["properties"] = map[string]any{}
		}
	}
	return schema
}

func convertToolChoice(raw json.RawMessage, tr *translation, backendName string) any {
	if len(raw) == 0 || strings.EqualFold(backendName, "deepseek") {
		// DeepSeek 的 tool_choice 兼容性差，上游实现同样不传。
		return nil
	}
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil
		}
		choice.Type = s
	}
	switch choice.Type {
	case "tool":
		name := tr.registerToolName(choice.Name, "tool_0")
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}
	case "any":
		return "required"
	case "auto":
		return "auto"
	case "none":
		return "none"
	default:
		return nil
	}
}

func normalizeToolName(name, fallback string) string {
	cleaned := strings.Trim(toolNameRe.ReplaceAllString(name, "_"), "_")
	if cleaned == "" {
		cleaned = fallback
	}
	if len(cleaned) > 64 {
		cleaned = cleaned[:64]
	}
	return cleaned
}

// OpenAI Chat Completions 非流式响应。
type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// openAIToAnthropic 把非流式 OpenAI 响应翻译成 Anthropic message。
func openAIToAnthropic(resp *openAIResponse, originalModel, requestID string, tr *translation) map[string]any {
	content := make([]map[string]any, 0, 2)
	hasToolUse := false
	finishReason := "stop"

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		finishReason = choice.FinishReason
		// reasoning_content 一律丢弃：Anthropic 兼容层看不到思考过程。
		if text := openAIContentText(choice.Message.Content); text != "" {
			content = append(content, map[string]any{"type": "text", "text": text})
		}
		for _, call := range choice.Message.ToolCalls {
			input := map[string]any{}
			if err := json.Unmarshal([]byte(call.Function.Arguments), &input); err != nil {
				input = map[string]any{"_raw": call.Function.Arguments}
			}
			id := call.ID
			if id == "" {
				id = "toolu_" + randomHex(12)
			}
			name := tr.originalToolName(call.Function.Name)
			hasToolUse = true
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": input,
			})
		}
	}

	stopReason := mapFinishReason(finishReason)
	if hasToolUse {
		stopReason = "tool_use"
	}

	return map[string]any{
		"id":            requestID,
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         originalModel,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  resp.Usage.PromptTokens,
			"output_tokens": resp.Usage.CompletionTokens,
		},
	}
}

// openAIContentText 兼容 string、null 与 [{type:text,text:...}] 三种 content。
func openAIContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return strings.Join(out, "")
	}
	return ""
}

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
}
