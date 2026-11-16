package main

import (
	"encoding/json"
	"fmt"
	"regexp"
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
}

var toolNameRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// anthropicToOpenAI 把 Anthropic Messages 请求翻译成 OpenAI Chat Completions 请求。
// stream 由 req.Stream 决定：M2 起流式请求会把 stream=true 透传给上游。
func anthropicToOpenAI(req *anthropicRequest, backendModel string, cfg *config, backendName string) (map[string]any, *translation, error) {
	messages := make([]map[string]any, 0, len(req.Messages)+1)
	tr := &translation{toolNames: map[string]string{}}

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

	if tools := translateTools(req.Tools, tr); len(tools) > 0 {
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

func translateUserMessage(msg anthropicMessage, imagePolicy string) []map[string]any {
	text, blocks, isText := decodeBlocks(msg.Content)
	if isText {
		if text == "" {
			return nil
		}
		return []map[string]any{{"role": "user", "content": text}}
	}

	var toolMessages []map[string]any
	var textParts []string
	var imageParts []map[string]any
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
				textParts = append(textParts, block.Text)
			}
		case "image", "image_url":
			url := openAIImageURL(block)
			if url == "" || imagePolicy == "omit" || (imagePolicy == "omit_inline" && strings.HasPrefix(url, "data:")) {
				omitted++
				continue
			}
			imageParts = append(imageParts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
			})
		}
	}

	joined := strings.Join(textParts, " ")
	var content any
	switch {
	case len(imageParts) > 0:
		parts := make([]map[string]any, 0, len(imageParts)+2)
		if joined != "" {
			parts = append(parts, map[string]any{"type": "text", "text": joined})
		}
		parts = append(parts, imageParts...)
		if omitted > 0 {
			parts = append(parts, map[string]any{
				"type": "text",
				"text": fmt.Sprintf("[%d inline image attachment(s) omitted for backend compatibility.]", omitted),
			})
		}
		content = parts
	case omitted > 0:
		note := fmt.Sprintf("[%d inline image attachment(s) omitted for backend compatibility.]", omitted)
		content = strings.TrimSpace(strings.Join([]string{joined, note}, " "))
	default:
		content = joined
	}

	out := toolMessages
	if content != "" {
		out = append(out, map[string]any{"role": "user", "content": content})
	}
	return out
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
			name := block.Name
			if name == "" {
				name = "tool_0"
			}
			safe := normalizeToolName(name, "tool_0")
			tr.toolNames[safe] = name
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
	if block.Source == nil {
		return ""
	}
	if block.Source.URL != "" {
		return block.Source.URL
	}
	if block.Source.Data == "" {
		return ""
	}
	mediaType := block.Source.MediaType
	if mediaType == "" {
		mediaType = "image/png"
	}
	return "data:" + mediaType + ";base64," + block.Source.Data
}

func imagePolicyFor(backendName, configured string) string {
	switch strings.ToLower(strings.TrimSpace(configured)) {
	case "preserve", "omit", "omit_inline":
		return strings.ToLower(strings.TrimSpace(configured))
	}
	// auto：DeepSeek 官方接口的历史行为是不接受图片，其余上游按原样转发。
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
		original := tool.Name
		if original == "" {
			original = fallback
		}
		safe := normalizeToolName(original, fallback)
		tr.toolNames[safe] = original
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
		name := choice.Name
		for safe, original := range tr.toolNames {
			if original == name {
				name = safe
				break
			}
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": normalizeToolName(name, "tool_0")}}
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
			name := call.Function.Name
			if original, ok := tr.toolNames[name]; ok && original != "" {
				name = original
			}
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
