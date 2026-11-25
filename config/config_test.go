package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTemp writes raw JSON into a fresh file and returns its path.
func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestDefaultPathIsNextToExecutable(t *testing.T) {
	t.Setenv(EnvConfigPath, "")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(filepath.Dir(exe), FileName)
	if got != want {
		t.Fatalf("DefaultPath = %q, want %q", got, want)
	}
	if PathFor("/opt/app") != filepath.Join("/opt/app", "config.json") {
		t.Fatalf("PathFor = %q", PathFor("/opt/app"))
	}
}

func TestDefaultPathEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigPath, filepath.Join(dir, "custom.json"))
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if got != filepath.Join(dir, "custom.json") {
		t.Fatalf("DefaultPath = %q", got)
	}
}

func TestLoadOrCreateWritesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, created, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if !created {
		t.Fatal("LoadOrCreate did not report creation")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 600", perm)
	}

	if len(cfg.Forward.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(cfg.Forward.Targets))
	}
	target := cfg.Forward.Targets[0]
	if target.SSHPort != 22 || target.SSHUser != "root" || target.Command != "claude-science url" {
		t.Fatalf("target defaults = %+v", target)
	}
	if len(target.PortForwards) != 2 || target.PortForwards[0].Local != 9876 || target.PortForwards[1].Local != 9981 {
		t.Fatalf("port forwards = %+v", target.PortForwards)
	}
	if cfg.Local.DefaultBackend != "deepseek" || cfg.Local.ForceModel != "deepseek-flash" {
		t.Fatalf("local defaults = %+v", cfg.Local)
	}
	if cfg.Local.ProxyHost != "127.0.0.1" || cfg.Local.ProxyPort != 9876 {
		t.Fatalf("proxy defaults = %s:%d", cfg.Local.ProxyHost, cfg.Local.ProxyPort)
	}
	if cfg.Local.ClaudeSciencePath != "claude-science" || cfg.Local.ClaudeSciencePort != 9981 {
		t.Fatalf("claude-science defaults = %s:%d", cfg.Local.ClaudeSciencePath, cfg.Local.ClaudeSciencePort)
	}
	if cfg.Local.InlineImagePolicy != "omit_inline" {
		t.Fatalf("inline image policy = %q", cfg.Local.InlineImagePolicy)
	}
	if !cfg.Local.OpenBrowser {
		t.Fatal("open_browser default = false, want true")
	}
	if cfg.Local.Backends["deepseek"].BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("deepseek base url = %q", cfg.Local.Backends["deepseek"].BaseURL)
	}

	// A second load must not report creation and must see the same values.
	again, created, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if created {
		t.Fatal("second LoadOrCreate reported creation")
	}
	if !reflect.DeepEqual(cfg, again) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", again, cfg)
	}
}

func TestLoadFillsDefaults(t *testing.T) {
	path := writeTemp(t, `{
	  "forward": {"targets": [{"name": "box", "ssh_host": "192.0.2.1", "ssh_key": "/k"}]},
	  "local": {"backends": {"custom": {"api_key": "sk-live"}}}
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	target := cfg.Forward.Targets[0]
	if target.SSHPort != 22 || target.SSHUser != "root" || target.Command != "claude-science url" {
		t.Fatalf("target defaults not filled: %+v", target)
	}
	if len(target.PortForwards) != 2 {
		t.Fatalf("port forwards = %+v", target.PortForwards)
	}
	if cfg.Local.DefaultBackend != "deepseek" || cfg.Local.ForceModel != "deepseek-flash" {
		t.Fatalf("local defaults not filled: %+v", cfg.Local)
	}
	if !cfg.Local.OpenBrowser {
		t.Fatal("open_browser should default to true when absent")
	}
	if cfg.Local.Backends["custom"].APIKey != "sk-live" {
		t.Fatalf("custom key lost: %+v", cfg.Local.Backends["custom"])
	}
	if cfg.Local.Backends["custom"].BaseURL != "" && cfg.Local.Backends["custom"].BaseURL != "https://api.deepseek.com/v1" {
		// custom has no provider default; only deepseek/openai get one.
		t.Fatalf("unexpected custom base url %q", cfg.Local.Backends["custom"].BaseURL)
	}
}

// TestImageFallbackRoundTrip 断言 config.json 的 image_fallback 块能原样存取，
// 且缺省时保持零值（fallback 不生效），不会因为 ApplyDefaults 被悄悄打开。
func TestImageFallbackRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Local.DefaultBackend = "deepseek"
	cfg.Local.ImageFallback = ImageFallback{Mode: "auto", Backend: "openai", Model: "gpt-4o"}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"image_fallback"`) {
		t.Fatalf("saved config missing image_fallback:\n%s", data)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Local.ImageFallback != cfg.Local.ImageFallback {
		t.Fatalf("round-trip image_fallback = %+v, want %+v", got.Local.ImageFallback, cfg.Local.ImageFallback)
	}

	// 老配置没有这个块：Load 不能凭空填出 fallback 目标。
	legacyPath := writeTemp(t, `{"local": {"default_backend": "deepseek"}}`)
	legacy, err := Load(legacyPath)
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if legacy.Local.ImageFallback != (ImageFallback{}) {
		t.Fatalf("legacy config gained an image fallback: %+v", legacy.Local.ImageFallback)
	}
}

func TestLoadKeepsExplicitlyEmptyPortForwards(t *testing.T) {
	path := writeTemp(t, `{
	  "forward": {"targets": [{"name": "box", "ssh_host": "192.0.2.1", "ssh_key": "/k", "port_forwards": []}]}
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Forward.Targets[0].PortForwards; got == nil || len(got) != 0 {
		t.Fatalf("explicit empty port_forwards was replaced with %+v", got)
	}
}

func TestLoadLegacySingleTarget(t *testing.T) {
	path := writeTemp(t, `{
	  "name": "old",
	  "ssh_host": "192.0.2.10",
	  "ssh_key": "/home/me/.ssh/id_ed25519",
	  "container_id": "claude-science",
	  "command": "claude-science url"
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Forward.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(cfg.Forward.Targets))
	}
	target := cfg.Forward.Targets[0]
	if target.Name != "old" || target.SSHHost != "192.0.2.10" || target.SSHKey != "/home/me/.ssh/id_ed25519" {
		t.Fatalf("legacy target = %+v", target)
	}
	if target.SSHPort != 22 || target.SSHUser != "root" {
		t.Fatalf("legacy defaults = %+v", target)
	}
	if cfg.Local.ProxyPort != 9876 || cfg.Local.DefaultBackend != "deepseek" {
		t.Fatalf("local defaults missing for legacy config: %+v", cfg.Local)
	}
}

func TestLoadLegacyTargetsArray(t *testing.T) {
	path := writeTemp(t, `{
	  "targets": [
	    {"name": "a", "ssh_host": "192.0.2.20", "ssh_key": "/a"},
	    {"name": "b", "ssh_host": "192.0.2.21", "ssh_key": "/b", "ssh_port": 2222}
	  ]
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Forward.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(cfg.Forward.Targets))
	}
	if cfg.Forward.Targets[1].SSHPort != 2222 {
		t.Fatalf("second target = %+v", cfg.Forward.Targets[1])
	}
}

func TestTwoSectionsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.Forward.Targets = []Target{
		{
			Name:         "prod",
			SSHHost:      "203.0.113.5",
			SSHPort:      2200,
			SSHUser:      "ubuntu",
			SSHKey:       "/keys/id_ed25519",
			ContainerID:  "cs",
			Command:      "claude-science url",
			PortForwards: []PortForward{{Local: 9981, Remote: 9981}},
		},
	}
	cfg.Local = Local{
		DefaultBackend:    "custom",
		ForceModel:        "deepseek-chat",
		ModelAliases:      []ModelAlias{{ID: "claude-sonnet-4-5", DisplayName: "DS", Backend: "deepseek", Model: "deepseek-chat"}},
		ModelListMode:     "aliases",
		InlineImagePolicy: "preserve",
		ProxyHost:         "0.0.0.0",
		ProxyPort:         18766,
		ClaudeSciencePath: "/usr/local/bin/claude-science",
		ClaudeSciencePort: 19981,
		OpenBrowser:       false,
		Backends: map[string]Backend{
			"deepseek": {APIKey: "ds", BaseURL: "https://api.deepseek.com/v1", UpstreamMode: "openai", ModelPattern: "deepseek"},
			"openai":   {APIKey: "oa", BaseURL: "https://api.openai.com/v1", UpstreamMode: "openai"},
			"custom":   {APIKey: "cu", BaseURL: "https://example.test/v1", UpstreamMode: "openai", ModelPattern: "^m"},
		},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"forward"`, `"local"`, `"ssh_host"`, `"claude_science_path"`, `"api_key"`} {
		if !strings.Contains(text, want) {
			t.Errorf("saved config missing %s", want)
		}
	}
	if strings.Contains(text, `"targets"`+"\n") && strings.HasPrefix(strings.TrimSpace(text), "[") {
		t.Fatal("saved config is not an object")
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Load applies defaults; cfg is already complete, so they must match.
	if !reflect.DeepEqual(cfg, got) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, cfg)
	}
	if got.Local.OpenBrowser {
		t.Fatal("open_browser=false was not preserved")
	}
}

func TestLoadRejectsBrokenJSON(t *testing.T) {
	path := writeTemp(t, `{"forward":`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted broken JSON")
	}
}

func TestEffectiveBackendEnvFallback(t *testing.T) {
	t.Setenv("CUSTOM_API_KEY", "from-env")
	l := Default().Local
	l.Backends["custom"] = Backend{}
	b, ok := l.EffectiveBackend("custom")
	if !ok {
		t.Fatal("custom backend missing")
	}
	if b.APIKey != "from-env" {
		t.Fatalf("APIKey = %q, want from-env", b.APIKey)
	}

	l.Backends["custom"] = Backend{APIKey: "from-file"}
	b, _ = l.EffectiveBackend("CUSTOM")
	if b.APIKey != "from-file" {
		t.Fatalf("APIKey = %q, want from-file", b.APIKey)
	}
}

func TestUnknownFieldsAreIgnored(t *testing.T) {
	path := writeTemp(t, `{"forward":{"targets":[]},"local":{},"something_else":42}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Local.ProxyPort != 9876 {
		t.Fatalf("defaults not applied: %+v", cfg.Local)
	}
}

func TestSavedJSONIsIndentedObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Default()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("saved config is not valid JSON: %v", err)
	}
	if _, ok := decoded["forward"]; !ok {
		t.Fatal("saved config has no forward section")
	}
	if _, ok := decoded["local"]; !ok {
		t.Fatal("saved config has no local section")
	}
}
