package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	defaultProxyHost = "0.0.0.0"
	defaultProxyPort = 9876
)

// backend 描述一个 OpenAI 兼容上游。M1 只支持 OpenAI 协议；
// *_UPSTREAM_MODE=anthropic 会在启动时告警并仍按 OpenAI 处理。
type backend struct {
	name         string
	baseURL      string
	apiKey       string
	rawMode      string
	modelPattern *regexp.Regexp
}

// modelAlias 是 Claude 面向的模型 id 到真实上游模型的映射。
type modelAlias struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Backend     string `json:"backend"`
	Model       string `json:"model"`
}

// config 全部来自环境变量，.env 是唯一真源（不读 config.json）。
type config struct {
	DefaultBackend    string
	ForceModel        string
	ModelAliases      []modelAlias
	ModelListMode     string
	InlineImagePolicy string
	ProxyHost         string
	ProxyPort         int

	backends map[string]*backend
	warnings []string
}

// resolve 返回本次请求要用的上游与映射后的模型名。
//
// 解析顺序：
//  1. alias 命中：用 alias 的 backend（未写则 DEFAULT_BACKEND）与 model。
//  2. 某后端的 *_MODEL_PATTERN 命中且该后端已配置 key 与 base URL：用该后端，
//     模型名原样透传（例如配了 OPENAI_API_KEY 时 gpt-4o 就发给 OpenAI 的 gpt-4o）。
//  3. 其余请求：DEFAULT_BACKEND + FORCE_MODEL（FORCE_MODEL 为空则透传请求值）。
//     pattern 命中但后端缺 key 的请求也走这里：回落到默认后端仍可正常对话，
//     不会因为菜单里点了一个内置模型就 400。
func (c *config) resolve(model string) (*backend, string, error) {
	name := strings.ToLower(c.DefaultBackend)
	mapped := ""

	if alias := c.aliasFor(model); alias != nil {
		if alias.Backend != "" {
			name = strings.ToLower(alias.Backend)
		}
		if alias.Model != "" {
			mapped = alias.Model
		}
	} else {
		patternMatched := false
		for _, candidate := range []string{"deepseek", "openai", "custom"} {
			b, ok := c.backends[candidate]
			if ok && b.modelPattern != nil && b.modelPattern.MatchString(model) {
				name = candidate
				patternMatched = true
				break
			}
		}
		switch {
		case patternMatched && c.backendUsable(c.backends[name]):
			// 后端可用：请求的模型名就是该后端的真实模型名，不能再被
			// FORCE_MODEL 改写成别的厂商的模型。
			mapped = model
		case patternMatched:
			// 命中但没有 API key：回落默认后端，而不是报 400。
			name = strings.ToLower(c.DefaultBackend)
		}
	}

	b, ok := c.backends[name]
	if !ok {
		return nil, "", fmt.Errorf("unsupported backend '%s'. Use deepseek, openai, or custom.", name)
	}
	if b.apiKey == "" {
		return nil, "", fmt.Errorf("no API key configured for backend '%s'. Set the matching *_API_KEY in .env.", name)
	}
	if b.baseURL == "" {
		return nil, "", fmt.Errorf("no base URL configured for backend '%s'. Set the matching *_BASE_URL in .env.", name)
	}

	if mapped == "" {
		mapped = c.ForceModel
	}
	if mapped == "" {
		mapped = model
	}
	return b, mapped, nil
}

// backendUsable 判断一个后端是否可以直接承接请求。
func (c *config) backendUsable(b *backend) bool {
	return b != nil && b.apiKey != "" && b.baseURL != ""
}

func (c *config) aliasFor(model string) *modelAlias {
	for i := range c.ModelAliases {
		if c.ModelAliases[i].ID == model {
			return &c.ModelAliases[i]
		}
	}
	return nil
}

// modelList 生成 GET /v1/models 的 data 数组。
func (c *config) modelList() []map[string]any {
	seen := map[string]bool{}
	out := make([]map[string]any, 0, len(c.ModelAliases)+len(builtinModels))

	addAliases := func() {
		for _, a := range c.ModelAliases {
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			out = append(out, map[string]any{"id": a.ID, "display_name": a.DisplayName, "type": "model"})
		}
	}
	addBuiltin := func() {
		for _, m := range builtinModels {
			id := m["id"].(string)
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, m)
		}
	}

	switch strings.ToLower(strings.TrimSpace(c.ModelListMode)) {
	case "aliases", "alias", "third_party", "third-party":
		// 只显示 alias 项；alias 清单为空时就是空清单，不回退到 builtin。
		addAliases()
	case "builtin", "builtins", "compat":
		addBuiltin()
	case "aliases_first", "aliases-first", "mixed":
		addAliases()
		addBuiltin()
	default:
		// 默认规则：配了 MODEL_ALIASES 就是 aliases（只有 alias 项），
		// 否则只有内置清单。不会回退到 aliases_first 把两者混在一起。
		if len(c.ModelAliases) > 0 {
			addAliases()
		} else {
			addBuiltin()
		}
	}
	return out
}

var builtinModels = []map[string]any{
	{"id": "claude-sonnet-4-5", "display_name": "Claude Sonnet 4.5", "type": "model"},
	{"id": "claude-opus-4-8", "display_name": "Claude Opus 4.8", "type": "model"},
	{"id": "claude-haiku-4-5-20251001", "display_name": "Claude Haiku 4.5", "type": "model"},
	{"id": "deepseek-flash", "display_name": "DeepSeek Flash", "type": "model"},
	{"id": "deepseek-chat", "display_name": "DeepSeek Chat", "type": "model"},
	{"id": "deepseek-reasoner", "display_name": "DeepSeek Reasoner", "type": "model"},
	{"id": "gpt-4o", "display_name": "GPT-4o", "type": "model"},
}

// newConfigFromEnv 读取环境变量并构造配置。
func newConfigFromEnv() *config {
	c := &config{
		DefaultBackend:    envOr("DEFAULT_BACKEND", "deepseek"),
		ForceModel:        strings.TrimSpace(os.Getenv("FORCE_MODEL")),
		ModelListMode:     strings.TrimSpace(os.Getenv("MODEL_LIST_MODE")),
		// 默认 omit_inline：默认模型 deepseek-flash 是纯文本模型，图片没有视觉出口；
		// 显式设 preserve 之前，贴图会被丢弃并补占位文本。
		InlineImagePolicy: strings.ToLower(envOr("INLINE_IMAGE_POLICY", "omit_inline")),
		ProxyHost:         envOr("PROXY_HOST", defaultProxyHost),
		ProxyPort:         envIntOr("PROXY_PORT", defaultProxyPort),
		backends:          map[string]*backend{},
	}
	c.ModelAliases = parseModelAliases(os.Getenv("MODEL_ALIASES"), c)

	for _, name := range []string{"deepseek", "openai", "custom"} {
		prefix := strings.ToUpper(name)
		b := &backend{
			name:    name,
			baseURL: normalizeBaseURL(os.Getenv(prefix + "_BASE_URL")),
			apiKey:  strings.TrimSpace(os.Getenv(prefix + "_API_KEY")),
			rawMode: envOr(prefix+"_UPSTREAM_MODE", "openai"),
		}
		if name == "openai" && b.baseURL == "" {
			b.baseURL = "https://api.openai.com/v1"
		}
		if name == "deepseek" && b.baseURL == "" {
			b.baseURL = "https://api.deepseek.com/v1"
		}
		if pattern := strings.TrimSpace(os.Getenv(prefix + "_MODEL_PATTERN")); pattern != "" {
			if re, err := regexp.Compile("(?i)" + pattern); err == nil {
				b.modelPattern = re
			} else {
				c.warnings = append(c.warnings, fmt.Sprintf("invalid %s_MODEL_PATTERN %q: %v", prefix, pattern, err))
			}
		}
		c.backends[name] = b
	}

	if c.backends["deepseek"].modelPattern == nil {
		c.backends["deepseek"].modelPattern = regexp.MustCompile(`(?i)deepseek|deep-seek`)
	}
	if c.backends["openai"].modelPattern == nil {
		c.backends["openai"].modelPattern = regexp.MustCompile(`(?i)^(gpt-|o1|o3|o4|chatgpt)`)
	}

	for _, name := range []string{"deepseek", "openai", "custom"} {
		b := c.backends[name]
		if mode := normalizeUpstreamMode(b.rawMode); mode != "openai" {
			c.warnings = append(c.warnings,
				fmt.Sprintf("%s_UPSTREAM_MODE=%s is not supported in M1; treating %s as an OpenAI-compatible upstream", strings.ToUpper(name), b.rawMode, name))
		}
	}
	return c
}

// parseModelAliases 接受 JSON 数组或 {"alias-id": {...}} 两种形式。
func parseModelAliases(raw string, c *config) []modelAlias {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var list []modelAlias
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		return normalizeAliases(list)
	}

	var byID map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &byID); err == nil {
		list = make([]modelAlias, 0, len(byID))
		for id, value := range byID {
			var a modelAlias
			if err := json.Unmarshal(value, &a); err != nil {
				var model string
				if err := json.Unmarshal(value, &model); err != nil {
					continue
				}
				a = modelAlias{Model: model}
			}
			if a.ID == "" {
				a.ID = id
			}
			list = append(list, a)
		}
		return normalizeAliases(list)
	}

	c.warnings = append(c.warnings, "MODEL_ALIASES is not valid JSON; ignoring it")
	return nil
}

func normalizeAliases(in []modelAlias) []modelAlias {
	out := make([]modelAlias, 0, len(in))
	for _, a := range in {
		a.ID = strings.TrimSpace(a.ID)
		if a.ID == "" {
			continue
		}
		a.Backend = strings.ToLower(strings.TrimSpace(a.Backend))
		if a.Backend != "" && a.Backend != "deepseek" && a.Backend != "openai" && a.Backend != "custom" {
			continue
		}
		a.Model = strings.TrimSpace(a.Model)
		if a.Model == "" {
			a.Model = a.ID
		}
		a.DisplayName = strings.TrimSpace(a.DisplayName)
		if a.DisplayName == "" {
			a.DisplayName = a.Model
		}
		out = append(out, a)
	}
	return out
}

func normalizeUpstreamMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "anthropic", "native", "passthrough":
		return "anthropic"
	default:
		return "openai"
	}
}

// normalizeBaseURL 返回 OpenAI 兼容的 /v1 基址，避免重复 /v1。
// 同时容忍用户把 .../v1/chat/completions 整条 URL 填进 *_BASE_URL。
func normalizeBaseURL(raw string) string {
	b := strings.TrimSpace(raw)
	if b == "" {
		return ""
	}
	b = strings.TrimRight(b, "/")
	for {
		trimmed := false
		for _, suffix := range []string{"/chat/completions", "/completions", "/messages"} {
			if strings.HasSuffix(b, suffix) {
				b = strings.TrimRight(strings.TrimSuffix(b, suffix), "/")
				trimmed = true
				break
			}
		}
		if !trimmed {
			break
		}
	}
	if b == "" {
		return ""
	}
	if !strings.HasSuffix(b, "/v1") {
		b += "/v1"
	}
	return b
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
