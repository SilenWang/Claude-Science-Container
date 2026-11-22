package gui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/SilenWang/Claude-Science-Container/config"
)

// The GUI edits a draft of the same config.json the CLI reads. Text fields
// bind to strings so a half-typed port does not vanish under the caret; the
// draft is parsed and written only when the reader asks to save (or starts
// something, which saves first). Fields the window does not show are carried
// over untouched.

// forwardForm is the editable copy of one config.Target.
type forwardForm struct {
	name      string
	host      string
	port      string
	user      string
	key       string
	container string
	command   string
	forwards  []forwardFormPort
	nextID    int
}

type forwardFormPort struct {
	// id keeps a row's widget state with the row when a sibling is removed.
	id     int
	local  string
	remote string
}

func forwardFormFrom(t config.Target) forwardForm {
	f := forwardForm{
		name:      t.Name,
		host:      t.SSHHost,
		port:      strconv.Itoa(t.SSHPort),
		user:      t.SSHUser,
		key:       t.SSHKey,
		container: t.ContainerID,
		command:   t.Command,
	}
	for _, pf := range t.PortForwards {
		f.forwards = append(f.forwards, forwardFormPort{
			id:     f.nextID,
			local:  strconv.Itoa(pf.Local),
			remote: strconv.Itoa(pf.Remote),
		})
		f.nextID++
	}
	return f
}

// apply writes the form back into t, reporting the first malformed number as
// a sentence beside the field's own label.
func (f forwardForm) apply(t *config.Target, label string) error {
	port, err := parsePort(f.port, "SSH 端口", label)
	if err != nil {
		return err
	}
	t.Name = strings.TrimSpace(f.name)
	t.SSHHost = strings.TrimSpace(f.host)
	t.SSHPort = port
	t.SSHUser = strings.TrimSpace(f.user)
	t.SSHKey = strings.TrimSpace(f.key)
	t.ContainerID = strings.TrimSpace(f.container)
	t.Command = strings.TrimSpace(f.command)

	forwards := make([]config.PortForward, 0, len(f.forwards))
	for i, pf := range f.forwards {
		local, err := parsePort(pf.local, "本地端口", fmt.Sprintf("%s 第 %d 条转发", label, i+1))
		if err != nil {
			return err
		}
		remote, err := parsePort(pf.remote, "远端端口", fmt.Sprintf("%s 第 %d 条转发", label, i+1))
		if err != nil {
			return err
		}
		forwards = append(forwards, config.PortForward{Local: local, Remote: remote})
	}
	t.PortForwards = forwards
	return nil
}

// localForm is the editable copy of config.Local's visible fields. Keys and
// base URLs are kept per backend so switching backends does not drop edits.
type localForm struct {
	backend     string
	keys        map[string]string
	bases       map[string]string
	forceModel  string
	proxyHost   string
	proxyPort   string
	csPath      string
	csPort      string
	openBrowser bool
}

func localFormFrom(l config.Local) localForm {
	f := localForm{
		backend:     l.DefaultBackend,
		keys:        map[string]string{},
		bases:       map[string]string{},
		forceModel:  l.ForceModel,
		proxyHost:   l.ProxyHost,
		proxyPort:   strconv.Itoa(l.ProxyPort),
		csPath:      l.ClaudeSciencePath,
		csPort:      strconv.Itoa(l.ClaudeSciencePort),
		openBrowser: l.OpenBrowser,
	}
	for _, name := range backendNames {
		b := l.Backends[name]
		f.keys[name] = b.APIKey
		f.bases[name] = b.BaseURL
	}
	return f
}

// apply writes the visible fields into l, leaving aliases and policies as the
// caller loaded them.
func (f localForm) apply(l *config.Local) error {
	proxyPort, err := parsePort(f.proxyPort, "代理端口", "本地启动")
	if err != nil {
		return err
	}
	csPort, err := parsePort(f.csPort, "Claude Science 端口", "本地启动")
	if err != nil {
		return err
	}

	l.DefaultBackend = f.backend
	l.ForceModel = strings.TrimSpace(f.forceModel)
	l.ProxyHost = strings.TrimSpace(f.proxyHost)
	if l.ProxyHost == "" {
		l.ProxyHost = "127.0.0.1"
	}
	l.ProxyPort = proxyPort
	l.ClaudeSciencePath = strings.TrimSpace(f.csPath)
	l.ClaudeSciencePort = csPort
	l.OpenBrowser = f.openBrowser

	if l.Backends == nil {
		l.Backends = map[string]config.Backend{}
	}
	for _, name := range backendNames {
		b := l.Backends[name]
		b.APIKey = strings.TrimSpace(f.keys[name])
		b.BaseURL = strings.TrimSpace(f.bases[name])
		l.Backends[name] = b
	}
	return nil
}

var backendNames = []string{"deepseek", "openai", "custom"}

var backendLabels = []string{"DeepSeek", "OpenAI", "自定义（OpenAI 兼容）"}

func backendFromLabel(label string) string {
	for i, l := range backendLabels {
		if l == label {
			return backendNames[i]
		}
	}
	return "deepseek"
}

func labelForBackend(name string) string {
	for i, n := range backendNames {
		if n == name {
			return backendLabels[i]
		}
	}
	return backendLabels[0]
}

func parsePort(value, field, where string) (int, error) {
	value = strings.TrimSpace(value)
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s：%s 不是有效端口（1-65535）", where, field)
	}
	return n, nil
}

// cloneConfig copies the loaded config deeply enough that edits to the draft
// never reach the running services' copy.
func cloneConfig(cfg *config.Config) *config.Config {
	out := &config.Config{Forward: config.Forward{Targets: append([]config.Target(nil), cfg.Forward.Targets...)}}
	out.Local = cfg.Local
	if cfg.Local.Backends != nil {
		out.Local.Backends = make(map[string]config.Backend, len(cfg.Local.Backends))
		for k, v := range cfg.Local.Backends {
			out.Local.Backends[k] = v
		}
	}
	if cfg.Local.ModelAliases != nil {
		out.Local.ModelAliases = append([]config.ModelAlias(nil), cfg.Local.ModelAliases...)
	}
	return out
}

// candidate builds the config the draft would save, plus every validation
// problem that stops it.
func (a *App) candidate() (*config.Config, []string) {
	cfg := cloneConfig(a.cfg)
	targets := make([]config.Target, 0, len(a.targets))
	var problems []string
	for _, item := range a.targets {
		t := config.Target{}
		if err := item.form.apply(&t, item.label()); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		targets = append(targets, t)
	}
	cfg.Forward.Targets = targets
	if err := a.local.form.apply(&cfg.Local); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		// Still return a config with whatever parsed, so a dirty check can
		// compare it; callers only save when problems is empty.
		return cfg, problems
	}
	return cfg, nil
}

// dirty reports whether the draft differs from what is on disk.
func (a *App) dirty() bool {
	if a.err != nil || a.cfg == nil {
		return false
	}
	cand, problems := a.candidate()
	if len(problems) > 0 {
		return true
	}
	left, err1 := json.Marshal(cand)
	right, err2 := json.Marshal(a.cfg)
	if err1 != nil || err2 != nil {
		return true
	}
	return string(left) != string(right)
}
