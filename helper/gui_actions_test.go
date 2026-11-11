package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Clicking 连接 starts a session for that card and leaves the others alone.
// The session will fail to reach the test's fake host and retry; it is
// stopped before the test ends.
func TestClickConnectStartsOnlyItsTarget(t *testing.T) {
	app := &guiApp{configPath: writeTempConfig(t, `{
		"targets": [
			{"name": "alpha", "ssh_host": "127.0.0.1", "ssh_port": 1, "ssh_key": "/nonexistent"},
			{"name": "beta", "ssh_host": "127.0.0.1", "ssh_port": 1, "ssh_key": "/nonexistent"}
		]
	}`)}
	app.reload()

	tt := ui.NewTester(app.view, 600, 480)
	if err := tt.Click("连接"); err != nil {
		t.Fatal(err)
	}

	if app.items[0].sess == nil || app.items[0].phase == phaseIdle {
		t.Fatal("the clicked card did not start")
	}
	if app.items[1].sess != nil || app.items[1].phase != phaseIdle {
		t.Fatal("a card that was not clicked started too")
	}
	defer app.items[0].sess.Stop()
}

// Clicking the body of an idle card does what its button does.
func TestClickCardBodyConnects(t *testing.T) {
	path := writeTempConfig(t, `{
		"targets": [{"name": "alpha", "ssh_host": "127.0.0.1", "ssh_port": 1, "ssh_key": "/nonexistent"}]
	}`)
	app := &guiApp{configPath: path}
	app.reload()

	tt := ui.NewTester(app.view, 600, 480)
	if err := tt.Click("alpha"); err != nil {
		t.Fatal(err)
	}
	if app.items[0].sess == nil {
		t.Fatal("clicking the card body did not start the target")
	}
	defer app.items[0].sess.Stop()
}

// Clicking 停止 drops the session and puts the card back to idle.
func TestClickStopReturnsToIdle(t *testing.T) {
	app := &guiApp{configPath: writeTempConfig(t, `{
		"targets": [{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"}]
	}`)}
	app.reload()

	app.items[0].phase = phaseError
	app.items[0].status = "无法连接，3 秒后重试"
	app.items[0].detail = "dial: refused"
	s := newSession(app.items[0].target)
	app.items[0].sess = s

	tt := ui.NewTester(app.view, 600, 480)
	if err := tt.Click("停止"); err != nil {
		t.Fatal(err)
	}

	if app.items[0].sess != nil || app.items[0].phase != phaseIdle {
		t.Fatalf("card after 停止: phase=%v sess=%v", app.items[0].phase, app.items[0].sess)
	}
	if app.items[0].detail != "" {
		t.Fatalf("the failure text survived 停止: %q", app.items[0].detail)
	}
	if !s.stopped() {
		t.Fatal("the session was not stopped")
	}
	if !hasLabel(tt, "连接") {
		t.Fatalf("no 连接 action after 停止; texts: %v", tt.Texts())
	}
}

// The detail toggle opens and closes, so a reader can look and then put the
// card back as it was.
func TestDetailToggleRoundTrip(t *testing.T) {
	app := &guiApp{configPath: writeTempConfig(t, `{
		"targets": [{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"}]
	}`)}
	app.reload()
	app.items[0].phase = phaseError
	app.items[0].status = "无法连接，3 秒后重试"
	app.items[0].detail = "dial tcp 10.0.0.1:22: connection refused"

	tt := ui.NewTester(app.view, 600, 480)
	if tt.HasText("connection refused") {
		t.Fatal("detail shows before it is asked for")
	}
	if err := tt.Click("详细信息"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("connection refused") {
		t.Fatalf("detail did not open; texts: %v", tt.Texts())
	}
	if err := tt.Click("详细信息"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("connection refused") {
		t.Fatalf("detail did not close; texts: %v", tt.Texts())
	}
}
