package gui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/SilenWang/Claude-Science-Container/config"
)

// Removing one port-forward row leaves its siblings alone, including the
// values already typed into them.
func TestRemoveForwardRowKeepsOtherRows(t *testing.T) {
	path := writeTempConfig(t, `{
		"forward": {"targets": [{
			"name": "alpha", "ssh_host": "10.0.0.1", "ssh_user": "root", "ssh_key": "/k",
			"port_forwards": [{"local": 18000, "remote": 8000}, {"local": 19981, "remote": 9981}]
		}]}
	}`)
	app := &App{configPath: path}
	app.reload()
	app.targets[0].expanded = true

	tt := ui.NewTesterWith(app.view, 760, 900, ui.TesterOptions{})
	if err := tt.Click("移除"); err != nil {
		t.Fatal(err)
	}

	rows := app.targets[0].form.forwards
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].local != "19981" || rows[0].remote != "9981" {
		t.Fatalf("the wrong row was removed: %+v", rows)
	}

	if !app.save() {
		t.Fatalf("save failed: %s", app.bannerDetail)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Forward.Targets[0].PortForwards; len(got) != 1 || got[0].Local != 19981 || got[0].Remote != 9981 {
		t.Fatalf("port forwards = %+v", got)
	}
}

// Removing every row is a real choice: an empty list means "tunnel only",
// and reloading does not bring the default pair back.
func TestRemoveAllForwardRowsPersists(t *testing.T) {
	path := writeTempConfig(t, `{
		"forward": {"targets": [{
			"name": "alpha", "ssh_host": "10.0.0.1", "ssh_user": "root", "ssh_key": "/k",
			"port_forwards": [{"local": 9981, "remote": 9981}]
		}]}
	}`)
	app := &App{configPath: path}
	app.reload()
	app.targets[0].expanded = true

	tt := ui.NewTesterWith(app.view, 760, 900, ui.TesterOptions{})
	if err := tt.Click("移除"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("没有端口转发") {
		t.Fatalf("empty-port hint missing; texts: %v", tt.Texts())
	}
	if !app.save() {
		t.Fatalf("save failed: %s", app.bannerDetail)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Forward.Targets[0].PortForwards; len(got) != 0 {
		t.Fatalf("port forwards came back after save: %+v", got)
	}

	app.reload()
	if len(app.targets[0].form.forwards) != 0 {
		t.Fatalf("reload restored %d rows", len(app.targets[0].form.forwards))
	}
}

// Removing the first server keeps the second one's values, not the first
// one's, on screen and in the draft.
func TestDeleteServerKeepsRemainingValues(t *testing.T) {
	path := writeTempConfig(t, twoServers)
	app := &App{configPath: path}
	app.reload()
	app.targets[0].expanded = true

	tt := ui.NewTesterWith(app.view, 760, 1200, ui.TesterOptions{})
	if err := tt.Click("删除这台服务器"); err != nil {
		t.Fatal(err)
	}

	if len(app.targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(app.targets))
	}
	if got := app.targets[0].form; got.name != "beta" || got.host != "10.0.0.2" || got.user != "deploy" {
		t.Fatalf("remaining card = %+v", got)
	}
	if !tt.HasText("beta") || !tt.HasText("deploy@10.0.0.2") {
		t.Fatalf("remaining card is not shown; texts: %v", tt.Texts())
	}
}
