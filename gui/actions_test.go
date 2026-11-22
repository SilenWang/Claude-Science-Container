package gui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/SilenWang/Claude-Science-Container/config"
)

// Clicking 连接 saves the draft and starts that card's session only. The
// session cannot reach the test's fake host and keeps retrying; it is stopped
// before the test ends.
func TestConnectSavesAndStartsOnlyItsTarget(t *testing.T) {
	path := writeTempConfig(t, `{
		"forward": {"targets": [
			{"name": "alpha", "ssh_host": "127.0.0.1", "ssh_port": 1, "ssh_key": "/nonexistent"},
			{"name": "beta", "ssh_host": "127.0.0.1", "ssh_port": 1, "ssh_key": "/nonexistent"}
		]}
	}`)
	app := &App{configPath: path}
	app.reload()
	app.targets[0].form.host = "127.0.0.2"

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}

	if app.targets[0].sess == nil || app.targets[0].phase == phaseFromString("idle") {
		t.Fatal("the clicked card did not start")
	}
	if app.targets[1].sess != nil {
		t.Fatal("a card that was not clicked started too")
	}
	defer app.targets[0].sess.Stop()

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Forward.Targets[0].SSHHost != "127.0.0.2" {
		t.Fatalf("connect did not save the draft first: host = %q", loaded.Forward.Targets[0].SSHHost)
	}
}

// Adding a server edits the draft, and saving writes it where the CLI reads.
func TestAddServerSaves(t *testing.T) {
	path := writeTempConfig(t, `{"forward":{"targets":[]}}`)
	app := &App{configPath: path}
	app.reload()

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	if err := tt.Click("添加服务器"); err != nil {
		t.Fatal(err)
	}
	if len(app.targets) != 1 {
		t.Fatalf("targets after adding = %d, want 1", len(app.targets))
	}
	app.targets[0].form.host = "192.0.2.10"
	app.targets[0].form.key = "/keys/new"
	if !app.save() {
		t.Fatalf("save failed: %s", app.bannerDetail)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Forward.Targets) != 1 || loaded.Forward.Targets[0].SSHHost != "192.0.2.10" {
		t.Fatalf("added target did not round-trip: %+v", loaded.Forward.Targets)
	}
}

// Deleting a server removes it from the draft and the next save drops it from
// the shared file.
func TestDeleteServerSaves(t *testing.T) {
	path := writeTempConfig(t, twoServers)
	app := &App{configPath: path}
	app.reload()

	app.targets[0].expanded = true
	tt := ui.NewTesterWith(app.view, 760, 1200, ui.TesterOptions{})
	if err := tt.Click("删除这台服务器"); err != nil {
		t.Fatal(err)
	}
	if len(app.targets) != 1 || app.targets[0].form.name != "beta" {
		t.Fatalf("delete left %d cards", len(app.targets))
	}
	if !app.save() {
		t.Fatal("save after delete failed")
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Forward.Targets) != 1 || loaded.Forward.Targets[0].Name != "beta" {
		t.Fatalf("delete did not reach disk: %+v", loaded.Forward.Targets)
	}
}

// Starting the local stack without a key explains what is missing instead of
// launching a doomed proxy.
func TestStartLocalNeedsAKey(t *testing.T) {
	app := &App{configPath: writeTempConfig(t, `{"forward":{"targets":[]}}`)}
	app.reload()
	app.tab = 1

	app.startLocal()
	if app.local.runner != nil {
		t.Fatal("a runner was started without an API key")
	}
	if app.bannerText == "" {
		t.Fatal("no explanation was surfaced")
	}

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	if !tt.HasText("请先填写 API Key") {
		t.Fatalf("banner missing from the window; texts: %v", tt.Texts())
	}
}

// Stopping when nothing runs is a no-op, and the tab keeps offering 启动.
func TestStopLocalWithoutRunnerIsQuiet(t *testing.T) {
	app := &App{configPath: writeTempConfig(t, `{"forward":{"targets":[]}}`)}
	app.reload()
	app.tab = 1

	app.stopLocal()
	if app.local.phase != "" && app.local.phase != phaseLocal("idle") {
		t.Fatalf("phase = %q", app.local.phase)
	}
	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	if !hasLabel(tt, "启动") {
		t.Fatalf("start action missing; texts: %v", tt.Texts())
	}
}
