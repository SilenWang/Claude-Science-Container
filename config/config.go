// Package config owns the single config.json shared by the forward and local
// commands (and, in the next phase, by the GUI). The file lives next to the
// executable so an unpacked build is self-contained.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the fixed file name looked up next to the executable.
const FileName = "config.json"

// EnvConfigPath lets callers (and tests) point at an explicit config file.
const EnvConfigPath = "CSC_CONFIG"

// PortForward is one local → remote TCP forward.
type PortForward struct {
	Local  int `json:"local"`
	Remote int `json:"remote"`
}

// Target is one SSH target, with the field names the old helper used.
type Target struct {
	Name         string        `json:"name"`
	SSHHost      string        `json:"ssh_host"`
	SSHPort      int           `json:"ssh_port"`
	SSHUser      string        `json:"ssh_user"`
	SSHKey       string        `json:"ssh_key"`
	ContainerID  string        `json:"container_id"`
	Command      string        `json:"command"`
	PortForwards []PortForward `json:"port_forwards"`
}

// Forward groups the SSH targets into the unified config.
type Forward struct {
	Targets []Target `json:"targets"`
}

// ModelAlias maps a Claude-facing model id to a real upstream model.
type ModelAlias struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Backend     string `json:"backend"`
	Model       string `json:"model"`
}

// Backend is one OpenAI-compatible upstream.
type Backend struct {
	APIKey       string `json:"api_key"`
	BaseURL      string `json:"base_url"`
	UpstreamMode string `json:"upstream_mode,omitempty"`
	ModelPattern string `json:"model_pattern,omitempty"`
}

// Local is everything the local command needs: the proxy backends plus how to
// launch claude-science against that proxy.
type Local struct {
	DefaultBackend    string             `json:"default_backend"`
	ForceModel        string             `json:"force_model"`
	ModelAliases      []ModelAlias       `json:"model_aliases"`
	ModelListMode     string             `json:"model_list_mode,omitempty"`
	InlineImagePolicy string             `json:"inline_image_policy"`
	ProxyHost         string             `json:"proxy_host"`
	ProxyPort         int                `json:"proxy_port"`
	ClaudeSciencePath string             `json:"claude_science_path"`
	ClaudeSciencePort int                `json:"claude_science_port"`
	OpenBrowser       bool               `json:"open_browser"`
	Backends          map[string]Backend `json:"backends"`
}

// Config is the whole config.json.
type Config struct {
	Forward Forward `json:"forward"`
	Local   Local   `json:"local"`
}

const (
	defaultProxyHost         = "127.0.0.1"
	defaultProxyPort         = 9876
	defaultClaudeSciencePort = 9981
	defaultClaudeSciencePath = "claude-science"
	defaultBackend           = "deepseek"
	defaultForceModel        = "deepseek-flash"
	defaultInlineImagePolicy = "omit_inline"
	defaultCommand           = "claude-science url"
)

// DefaultTarget returns the placeholder target a first-run config contains.
func DefaultTarget() Target {
	return Target{
		Name:    "default",
		SSHPort: 22,
		SSHUser: "root",
		Command: defaultCommand,
		PortForwards: []PortForward{
			{Local: 9876, Remote: 9876},
			{Local: 9981, Remote: 9981},
		},
	}
}

// Default returns a complete, valid config with every default filled in.
func Default() *Config {
	return &Config{
		Forward: Forward{Targets: []Target{DefaultTarget()}},
		Local: Local{
			DefaultBackend:    defaultBackend,
			ForceModel:        defaultForceModel,
			ModelAliases:      []ModelAlias{},
			InlineImagePolicy: defaultInlineImagePolicy,
			ProxyHost:         defaultProxyHost,
			ProxyPort:         defaultProxyPort,
			ClaudeSciencePath: defaultClaudeSciencePath,
			ClaudeSciencePort: defaultClaudeSciencePort,
			OpenBrowser:       true,
			Backends: map[string]Backend{
				"deepseek": {BaseURL: "https://api.deepseek.com/v1", UpstreamMode: "openai"},
				"openai":   {BaseURL: "https://api.openai.com/v1", UpstreamMode: "openai"},
				"custom":   {UpstreamMode: "openai"},
			},
		},
	}
}

// DefaultPath resolves config.json next to the running executable.
// CSC_CONFIG overrides it, which is how tests and power users relocate it.
func DefaultPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvConfigPath)); p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("resolving %s=%q: %w", EnvConfigPath, p, err)
		}
		return abs, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), FileName), nil
}

// PathFor returns the config path for a given executable directory.
func PathFor(exeDir string) string {
	return filepath.Join(exeDir, FileName)
}

// Load reads path, decodes legacy or unified shapes, and fills defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	cfg.ApplyDefaults()
	return &cfg, nil
}

// LoadOrCreate creates the file with defaults when it does not exist yet.
// The bool reports whether it was created.
func LoadOrCreate(path string) (*Config, bool, error) {
	cfg, err := Load(path)
	if err == nil {
		return cfg, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	cfg = Default()
	if err := Save(path, cfg); err != nil {
		return nil, false, err
	}
	return cfg, true, nil
}

// Save writes cfg atomically with 0600 permissions (the file holds API keys).
func Save(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("creating temporary config in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	// Rename keeps the temp file's mode, but chmod again for filesystems that
	// ignore CreateTemp's mode.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// ApplyDefaults fills zero fields without touching values the user set.
func (c *Config) ApplyDefaults() {
	for i := range c.Forward.Targets {
		applyTargetDefaults(&c.Forward.Targets[i])
	}
	c.Local.ApplyDefaults()
}

func applyTargetDefaults(t *Target) {
	if t.SSHPort == 0 {
		t.SSHPort = 22
	}
	if t.SSHUser == "" {
		t.SSHUser = "root"
	}
	if t.Command == "" {
		t.Command = defaultCommand
	}
	if t.PortForwards == nil {
		t.PortForwards = []PortForward{
			{Local: 9876, Remote: 9876},
			{Local: 9981, Remote: 9981},
		}
	}
}

// ApplyDefaults fills the local section's zero fields.
func (l *Local) ApplyDefaults() {
	if l.DefaultBackend == "" {
		l.DefaultBackend = defaultBackend
	}
	if l.ForceModel == "" {
		l.ForceModel = defaultForceModel
	}
	if l.InlineImagePolicy == "" {
		l.InlineImagePolicy = defaultInlineImagePolicy
	}
	if l.ProxyHost == "" {
		l.ProxyHost = defaultProxyHost
	}
	if l.ProxyPort == 0 {
		l.ProxyPort = defaultProxyPort
	}
	if l.ClaudeSciencePath == "" {
		l.ClaudeSciencePath = defaultClaudeSciencePath
	}
	if l.ClaudeSciencePort == 0 {
		l.ClaudeSciencePort = defaultClaudeSciencePort
	}
	if l.Backends == nil {
		l.Backends = map[string]Backend{}
	}
	for _, name := range []string{"deepseek", "openai", "custom"} {
		b, ok := l.Backends[name]
		if !ok {
			b = Backend{}
		}
		if b.UpstreamMode == "" {
			b.UpstreamMode = "openai"
		}
		if b.BaseURL == "" {
			switch name {
			case "deepseek":
				b.BaseURL = "https://api.deepseek.com/v1"
			case "openai":
				b.BaseURL = "https://api.openai.com/v1"
			}
		}
		l.Backends[name] = b
	}
}

// EffectiveBackend returns the resolved backend config with env fallbacks,
// which is what the local command and the GUI both show.
func (l *Local) EffectiveBackend(name string) (Backend, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	b, ok := l.Backends[name]
	if !ok {
		return Backend{}, false
	}
	if b.APIKey == "" {
		b.APIKey = strings.TrimSpace(os.Getenv(strings.ToUpper(name) + "_API_KEY"))
	}
	if b.BaseURL == "" {
		b.BaseURL = strings.TrimSpace(os.Getenv(strings.ToUpper(name) + "_BASE_URL"))
	}
	return b, true
}

// UnmarshalJSON accepts the unified {"forward":...,"local":...} shape, the
// legacy {"targets":[...]} shape, and the legacy single-target object.
func (c *Config) UnmarshalJSON(data []byte) error {
	var probe struct {
		Forward      *Forward      `json:"forward"`
		Local        *Local        `json:"local"`
		Targets      []Target      `json:"targets"`
		Name         string        `json:"name"`
		SSHHost      string        `json:"ssh_host"`
		SSHPort      int           `json:"ssh_port"`
		SSHUser      string        `json:"ssh_user"`
		SSHKey       string        `json:"ssh_key"`
		ContainerID  string        `json:"container_id"`
		Command      string        `json:"command"`
		PortForwards []PortForward `json:"port_forwards"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}

	*c = Config{}
	if probe.Forward != nil {
		c.Forward = *probe.Forward
	}
	if probe.Local != nil {
		c.Local = *probe.Local
	}

	switch {
	case probe.Forward != nil || probe.Local != nil:
		// Unified shape. A stray top-level "targets" is only used when the
		// forward section itself has no targets.
		if len(c.Forward.Targets) == 0 && len(probe.Targets) > 0 {
			c.Forward.Targets = probe.Targets
		}
	case len(probe.Targets) > 0:
		c.Forward.Targets = probe.Targets
	default:
		legacy := Target{
			Name:         probe.Name,
			SSHHost:      probe.SSHHost,
			SSHPort:      probe.SSHPort,
			SSHUser:      probe.SSHUser,
			SSHKey:       probe.SSHKey,
			ContainerID:  probe.ContainerID,
			Command:      probe.Command,
			PortForwards: probe.PortForwards,
		}
		if !isZeroTarget(legacy) {
			c.Forward.Targets = []Target{legacy}
		}
	}
	return nil
}

// UnmarshalJSON defaults open_browser to true when the key is absent, so a
// minimal {"local":{"backends":...}} config still opens the login page.
func (l *Local) UnmarshalJSON(data []byte) error {
	type localAlias Local
	*l = Local{}
	aux := struct {
		*localAlias
		OpenBrowser *bool `json:"open_browser"`
	}{localAlias: (*localAlias)(l)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.OpenBrowser != nil {
		l.OpenBrowser = *aux.OpenBrowser
	} else {
		l.OpenBrowser = true
	}
	return nil
}

func isZeroTarget(t Target) bool {
	return t.Name == "" && t.SSHHost == "" && t.SSHPort == 0 && t.SSHUser == "" &&
		t.SSHKey == "" && t.ContainerID == "" && t.Command == "" && len(t.PortForwards) == 0
}
