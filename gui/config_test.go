package gui

import (
	"os"
	"testing"

	"github.com/SilenWang/Claude-Science-Container/config"
	"github.com/SilenWang/Claude-Science-Container/forward"
)

// Saving the 转发 tab writes every field the tab shows, so the CLI's `forward`
// command starts the same tunnel.
func TestSaveWritesForwardTab(t *testing.T) {
	path := writeTempConfig(t, twoServers)
	app := &App{configPath: path}
	app.reload()

	item := app.targets[0]
	item.form.name = "production"
	item.form.host = "203.0.113.9"
	item.form.port = "2222"
	item.form.user = "ubuntu"
	item.form.key = "/home/me/.ssh/id_ed25519"
	item.form.container = "cs-prod"
	item.form.command = "claude-science url"
	item.form.forwards = []forwardFormPort{
		{id: 0, local: "18000", remote: "8000"},
		{id: 1, local: "19981", remote: "9981"},
	}
	item.form.nextID = 2

	if !app.save() {
		t.Fatalf("save failed: %s %s", app.bannerText, app.bannerDetail)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Forward.Targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(loaded.Forward.Targets))
	}
	got := loaded.Forward.Targets[0]
	if got.Name != "production" || got.SSHHost != "203.0.113.9" || got.SSHPort != 2222 ||
		got.SSHUser != "ubuntu" || got.SSHKey != "/home/me/.ssh/id_ed25519" ||
		got.ContainerID != "cs-prod" || got.Command != "claude-science url" {
		t.Fatalf("target round-trip = %+v", got)
	}
	if len(got.PortForwards) != 2 || got.PortForwards[0].Local != 18000 ||
		got.PortForwards[0].Remote != 8000 || got.PortForwards[1].Local != 19981 {
		t.Fatalf("port forwards = %+v", got.PortForwards)
	}
	if app.dirty() {
		t.Fatal("the draft is still dirty after a successful save")
	}
}

// Saving the 本地启动 tab writes the backend, proxy and claude-science fields
// the tab shows, and leaves fields it does not show untouched.
func TestSaveWritesLocalTab(t *testing.T) {
	path := writeTempConfig(t, `{
		"forward": {"targets": []},
		"local": {
			"inline_image_policy": "preserve",
			"model_list_mode": "static",
			"model_aliases": [{"id": "claude-x", "display_name": "Claude X", "backend": "custom", "model": "m"}],
			"backends": {"deepseek": {"api_key": "old", "base_url": "https://old.example/v1"}}
		}
	}`)
	app := &App{configPath: path}
	app.reload()

	app.local.form.backend = "custom"
	app.local.form.keys["custom"] = "sk-custom"
	app.local.form.bases["custom"] = "https://api.example.com/v1"
	app.local.form.forceModel = "qwen3-coder"
	app.local.form.proxyPort = "19876"
	app.local.form.csPort = "19981"
	app.local.form.csPath = "/opt/claude-science/claude-science"
	app.local.form.openBrowser = false

	if !app.save() {
		t.Fatalf("save failed: %s %s", app.bannerText, app.bannerDetail)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	l := loaded.Local
	if l.DefaultBackend != "custom" || l.ForceModel != "qwen3-coder" {
		t.Fatalf("backend/force model = %q/%q", l.DefaultBackend, l.ForceModel)
	}
	if l.ProxyPort != 19876 || l.ClaudeSciencePort != 19981 {
		t.Fatalf("ports = %d/%d", l.ProxyPort, l.ClaudeSciencePort)
	}
	if l.ClaudeSciencePath != "/opt/claude-science/claude-science" || l.OpenBrowser {
		t.Fatalf("path/browser = %q/%v", l.ClaudeSciencePath, l.OpenBrowser)
	}
	if b := l.Backends["custom"]; b.APIKey != "sk-custom" || b.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("custom backend = %+v", b)
	}
	if l.InlineImagePolicy != "preserve" || l.ModelListMode != "static" || len(l.ModelAliases) != 1 {
		t.Fatalf("fields the tab does not show were lost: %+v", l)
	}
}

// A config written by the CLI (config.Save or `config init`) shows up in the
// window: both tabs read the same file.
func TestCLIConfigShowsInGUI(t *testing.T) {
	cfg := config.Default()
	cfg.Forward.Targets = []config.Target{{
		Name: "from-cli", SSHHost: "198.51.100.7", SSHPort: 2200, SSHUser: "ops",
		SSHKey: "/keys/cli", Command: "claude-science url --json",
		PortForwards: []config.PortForward{{Local: 7000, Remote: 7001}},
	}}
	cfg.Local.DefaultBackend = "openai"
	cfg.Local.ForceModel = "gpt-5"
	cfg.Local.ProxyPort = 12345
	cfg.Local.ClaudeSciencePort = 15432
	cfg.Local.Backends["openai"] = config.Backend{APIKey: "sk-cli", BaseURL: "https://api.openai.com/v1", UpstreamMode: "openai"}

	path := writeTempConfig(t, `{}`)
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	app := &App{configPath: path}
	app.reload()
	if len(app.targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(app.targets))
	}
	item := app.targets[0]
	if item.form.name != "from-cli" || item.form.host != "198.51.100.7" || item.form.port != "2200" ||
		item.form.user != "ops" || item.form.key != "/keys/cli" || item.form.command != "claude-science url --json" {
		t.Fatalf("forward form = %+v", item.form)
	}
	if len(item.form.forwards) != 1 || item.form.forwards[0].local != "7000" || item.form.forwards[0].remote != "7001" {
		t.Fatalf("forwards form = %+v", item.form.forwards)
	}
	if app.local.form.backend != "openai" || app.local.form.forceModel != "gpt-5" ||
		app.local.form.proxyPort != "12345" || app.local.form.csPort != "15432" ||
		app.local.form.keys["openai"] != "sk-cli" ||
		app.local.form.bases["openai"] != "https://api.openai.com/v1" {
		t.Fatalf("local form = %+v", app.local.form)
	}
}

// Editing one tab and saving does not undo what the other tab saved.
func TestBothTabsShareOneFile(t *testing.T) {
	path := writeTempConfig(t, twoServers)
	app := &App{configPath: path}
	app.reload()

	app.targets[1].form.name = "renamed"
	if !app.save() {
		t.Fatal("forward save failed")
	}
	app.local.form.keys["deepseek"] = "sk-later"
	app.local.form.proxyPort = "17777"
	if !app.save() {
		t.Fatal("local save failed")
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Forward.Targets[1].Name != "renamed" {
		t.Fatalf("forward edit lost: %+v", loaded.Forward.Targets)
	}
	if loaded.Local.Backends["deepseek"].APIKey != "sk-later" || loaded.Local.ProxyPort != 17777 {
		t.Fatalf("local edit lost: %+v", loaded.Local)
	}
}

// A malformed port stops the save and names the field; the file on disk is
// left as it was.
func TestSaveRejectsInvalidPort(t *testing.T) {
	path := writeTempConfig(t, twoServers)
	app := &App{configPath: path}
	app.reload()

	app.local.form.proxyPort = "not-a-port"
	if app.save() {
		t.Fatal("saved a config with an invalid proxy port")
	}
	if app.bannerText == "" {
		t.Fatal("no error was surfaced")
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Local.ProxyPort != 9876 {
		t.Fatalf("the invalid save reached disk: port = %d", loaded.Local.ProxyPort)
	}
}

// Saving a card whose target did not change keeps its tunnel; editing the
// target drops it, because the old session no longer describes the card.
func TestSaveKeepsUnchangedTunnelOnly(t *testing.T) {
	path := writeTempConfig(t, twoServers)
	app := &App{configPath: path}
	app.reload()

	alpha, beta := app.targets[0], app.targets[1]
	alphaSession := forward.NewSession(alpha.target, forward.Options{})
	betaSession := forward.NewSession(beta.target, forward.Options{})
	alpha.sess, alpha.phase = alphaSession, forward.PhaseConnected
	beta.sess, beta.phase = betaSession, forward.PhaseConnected

	beta.form.host = "10.0.0.9"
	if !app.save() {
		t.Fatal("save failed")
	}
	if app.targets[0].sess != alphaSession {
		t.Fatal("the unchanged card lost its tunnel")
	}
	if app.targets[1].sess != nil {
		t.Fatal("the edited card kept its old tunnel")
	}
}

// The first launch creates config.json next to the executable; the GUI's
// reload path accepts that file and shows one placeholder server.
func TestReloadCreatesMissingConfig(t *testing.T) {
	path := writeTempConfig(t, `{}`)
	_ = os.Remove(path)

	app := &App{configPath: path}
	app.reload()
	if app.err != nil {
		t.Fatalf("reload after first run: %v", app.err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config was not created: %v", err)
	}
	if len(app.targets) != 1 {
		t.Fatalf("default targets = %d, want 1", len(app.targets))
	}
}
