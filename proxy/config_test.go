package proxy

import (
	"os"
	"strings"
	"testing"
)

// clearConfigEnv 清空所有会参与 newConfigFromEnv 的环境变量，让默认值可以
// 被单独断言（宿主环境里残留的 *_API_KEY 不影响结果）。
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DEFAULT_BACKEND", "FORCE_MODEL", "MODEL_ALIASES", "MODEL_LIST_MODE",
		"PROXY_HOST", "PROXY_PORT", "INLINE_IMAGE_POLICY",
		"DEEPSEEK_API_KEY", "DEEPSEEK_BASE_URL", "DEEPSEEK_UPSTREAM_MODE", "DEEPSEEK_MODEL_PATTERN",
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_UPSTREAM_MODE", "OPENAI_MODEL_PATTERN",
		"CUSTOM_API_KEY", "CUSTOM_BASE_URL", "CUSTOM_UPSTREAM_MODE", "CUSTOM_MODEL_PATTERN",
	} {
		t.Setenv(key, "")
	}
}

func TestDefaultConfigFromEnv(t *testing.T) {
	clearConfigEnv(t)
	cfg := newConfigFromEnv()

	if cfg.DefaultBackend != "deepseek" {
		t.Errorf("DefaultBackend = %q, want deepseek", cfg.DefaultBackend)
	}
	// SIL-952 第 2 条：默认图片策略必须与 .env.example / README 一致。
	if cfg.InlineImagePolicy != "omit_inline" {
		t.Errorf("InlineImagePolicy = %q, want omit_inline", cfg.InlineImagePolicy)
	}
	if cfg.ProxyHost != defaultProxyHost || cfg.ProxyPort != defaultProxyPort {
		t.Errorf("listen = %s:%d, want %s:%d", cfg.ProxyHost, cfg.ProxyPort, defaultProxyHost, defaultProxyPort)
	}
	if len(cfg.ModelAliases) != 0 {
		t.Errorf("ModelAliases = %#v, want nil (compose supplies the default alias)", cfg.ModelAliases)
	}
	if cfg.ForceModel != "" {
		t.Errorf("ForceModel = %q, want empty (compose supplies the default model)", cfg.ForceModel)
	}
	if got := cfg.backends["deepseek"].baseURL; got != "https://api.deepseek.com/v1" {
		t.Errorf("deepseek baseURL = %q", got)
	}
}

func TestDefaultModelInBuiltinList(t *testing.T) {
	for _, m := range builtinModels {
		if m["id"] == "deepseek-flash" {
			if m["display_name"] != "DeepSeek Flash" {
				t.Fatalf("deepseek-flash display_name = %#v", m["display_name"])
			}
			return
		}
	}
	t.Fatalf("builtinModels does not contain deepseek-flash: %#v", builtinModels)
}

// TestResolveRouting 覆盖 SIL-952 第 1 条：内置模型 ID（gpt-4o）在没有对应
// 后端 key 时回落到默认后端 + FORCE_MODEL，配了 key 时原样发给自己的后端。
func TestResolveRouting(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DEEPSEEK_API_KEY", "ds-key")
	t.Setenv("FORCE_MODEL", "deepseek-flash")
	cfg := newConfigFromEnv()

	cases := []struct {
		name        string
		model       string
		wantBackend string
		wantModel   string
	}{
		{"unmapped claude id falls back to default backend", "claude-sonnet-4-5", "deepseek", "deepseek-flash"},
		{"gpt-4o without OPENAI_API_KEY falls back instead of erroring", "gpt-4o", "deepseek", "deepseek-flash"},
		{"deepseek id routes to deepseek and passes through", "deepseek-chat", "deepseek", "deepseek-chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, model, err := cfg.resolve(tc.model)
			if err != nil {
				t.Fatalf("resolve(%q): %v", tc.model, err)
			}
			if b.name != tc.wantBackend || model != tc.wantModel {
				t.Fatalf("resolve(%q) = (%s, %s), want (%s, %s)", tc.model, b.name, model, tc.wantBackend, tc.wantModel)
			}
		})
	}

	// 配了 OPENAI_API_KEY 后，gpt-4o 发给 OpenAI 自己，不能改名成 deepseek-flash。
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	cfg = newConfigFromEnv()
	b, model, err := cfg.resolve("gpt-4o")
	if err != nil {
		t.Fatalf("resolve(gpt-4o) with OPENAI_API_KEY: %v", err)
	}
	if b.name != "openai" || model != "gpt-4o" {
		t.Fatalf("resolve(gpt-4o) = (%s, %s), want (openai, gpt-4o)", b.name, model)
	}

	// alias 永远优先于 pattern 与 FORCE_MODEL。
	t.Setenv("MODEL_ALIASES", `[{"id":"claude-haiku-4-5","display_name":"DeepSeek Flash","backend":"deepseek","model":"deepseek-flash"}]`)
	cfg = newConfigFromEnv()
	b, model, err = cfg.resolve("claude-haiku-4-5")
	if err != nil || b.name != "deepseek" || model != "deepseek-flash" {
		t.Fatalf("resolve(alias) = (%v, %s), err = %v", b, model, err)
	}

	// FORCE_MODEL 未设置时，无法映射的请求透传原模型名。
	t.Setenv("MODEL_ALIASES", "")
	t.Setenv("FORCE_MODEL", "")
	cfg = newConfigFromEnv()
	b, model, err = cfg.resolve("claude-whatever")
	if err != nil || b.name != "deepseek" || model != "claude-whatever" {
		t.Fatalf("resolve without FORCE_MODEL = (%v, %s), err = %v", b, model, err)
	}
}

// TestShippedDefaultConfiguration 断言容器实际交付的默认值：compose 与
// .env.example 里的默认模型必须是 deepseek-flash，默认图片策略必须是
// omit_inline，两者不能再和代码默认值漂移。
func TestShippedDefaultConfiguration(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}

	compose := read("../docker-compose.yml")
	composeChecks := []string{
		`FORCE_MODEL: "${FORCE_MODEL:-deepseek-flash}"`,
		`INLINE_IMAGE_POLICY: "${INLINE_IMAGE_POLICY:-omit_inline}"`,
		`"display_name":"DeepSeek Flash"`,
		`"model":"deepseek-flash"`,
		`IMAGE_FALLBACK_MODEL: "${IMAGE_FALLBACK_MODEL:-deepseek-flash}"`,
	}
	for _, want := range composeChecks {
		if !strings.Contains(compose, want) {
			t.Errorf("docker-compose.yml missing %q", want)
		}
	}
	if strings.Contains(compose, "deepseek-v4-flash-vision-exp") {
		t.Errorf("docker-compose.yml still references the old default model")
	}

	envExample := read("../.env.example")
	envChecks := []string{
		"# FORCE_MODEL=deepseek-flash",
		"# INLINE_IMAGE_POLICY=omit_inline",
		`"display_name":"DeepSeek Flash"`,
		"text-only",
	}
	for _, want := range envChecks {
		if !strings.Contains(envExample, want) {
			t.Errorf(".env.example missing %q", want)
		}
	}
	if strings.Contains(envExample, "deepseek-v4-flash-vision-exp") {
		t.Errorf(".env.example still references the old default model")
	}
}
