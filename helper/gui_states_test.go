package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// hasLabel reports whether the window shows s as a label of its own.
// HasText matches substrings, which would find 打开 inside the subtitle.
func hasLabel(tt *ui.Tester, s string) bool {
	for _, text := range tt.Texts() {
		if text == s {
			return true
		}
	}
	return false
}

// clickingCard builds the app with one target and lets the test click its
// card body, away from the button.
func clickingCard(t *testing.T, setup func(*guiApp)) *ui.Tester {
	t.Helper()

	app := &guiApp{configPath: writeTempConfig(t, `{
		"targets": [{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"}]
	}`)}
	app.reload()
	if setup != nil {
		setup(app)
	}

	tt := ui.NewTesterWith(app.view, 600, 480, ui.TesterOptions{})
	return tt
}

// The state line names the state in words, so a card never shows only a
// colored dot.
func TestCardStates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase sessionPhase
		want  []string
	}{
		{"idle", phaseIdle, []string{"连接"}},
		{"connecting", phaseConnecting, []string{"正在获取登录地址", "停止"}},
		{"error", phaseError, []string{"无法连接，3 秒后重试", "停止", "详细信息"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := clickingCard(t, func(app *guiApp) {
				app.items[0].phase = tc.phase
				app.items[0].status = map[sessionPhase]string{
					phaseIdle:       "",
					phaseConnecting: "正在获取登录地址",
					phaseError:      "无法连接，3 秒后重试",
				}[tc.phase]
				if tc.phase == phaseError {
					app.items[0].detail = "dial tcp 10.0.0.1:22: connection refused"
				}
			})
			for _, want := range tc.want {
				if !hasLabel(tt, want) {
					t.Fatalf("%q missing; texts: %v", want, tt.Texts())
				}
			}
		})
	}
}

// A connected target with a login URL offers both ways on: reopen the page,
// or drop the tunnel.
func TestCardConnectedOffersOpenAndDisconnect(t *testing.T) {
	tt := clickingCard(t, func(app *guiApp) {
		connectFake(app.items[0])
	})
	for _, want := range []string{"打开", "断开", "已连接"} {
		if !hasLabel(tt, want) {
			t.Fatalf("%q missing; texts: %v", want, tt.Texts())
		}
	}
	if hasLabel(tt, "连接") {
		t.Fatalf("a connected card offers 连接; texts: %v", tt.Texts())
	}
}

// A target with no login URL can only be disconnected: there is no page to
// open.
func TestCardConnectedWithoutURLOffersNoOpen(t *testing.T) {
	tt := clickingCard(t, func(app *guiApp) {
		app.items[0].sess = newSession(app.items[0].target)
		app.items[0].phase = phaseConnected
		app.items[0].status = "已连接 · 未获取到登录地址，请手动打开转发端口"
	})
	if hasLabel(tt, "打开") {
		t.Fatalf("a card without a URL offers 打开; texts: %v", tt.Texts())
	}
	if !hasLabel(tt, "断开") {
		t.Fatalf("断开 missing; texts: %v", tt.Texts())
	}
}

// Config values that are the user's business, not the window's, stay out:
// ports, keys, container names and commands.
func TestCardHidesTechnicalFields(t *testing.T) {
	app := &guiApp{configPath: writeTempConfig(t, `{
		"targets": [{
			"name": "alpha", "ssh_host": "10.0.0.1", "ssh_port": 2222,
			"ssh_key": "/secret/id_ed25519", "container_id": "claude-science-7",
			"command": "claude-science url --verbose"
		}]
	}`)}
	app.reload()

	tt := ui.NewTester(app.view, 600, 480)
	for _, leak := range []string{"2222", "/secret/id_ed25519", "claude-science-7", "--verbose"} {
		if tt.HasText(leak) {
			t.Fatalf("%q shows in the window; texts: %v", leak, tt.Texts())
		}
	}
}
