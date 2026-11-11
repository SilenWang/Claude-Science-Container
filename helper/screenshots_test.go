package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestScreenshots renders the window in each state it can be in, as PNGs, so
// the design can be reviewed without a desktop. It runs with the rest of the
// suite; -v lists where the files are. Set SHOTS_DIR to keep them somewhere
// findable.
func TestScreenshots(t *testing.T) {
	dir := os.Getenv("SHOTS_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	shot := func(name string, w, h int, opts ui.TesterOptions, setup func(*guiApp)) {
		t.Helper()

		app := &guiApp{configPath: writeTempConfig(t, `{
			"targets": [
				{"name": "production", "ssh_host": "192.168.1.100", "ssh_key": "/k"},
				{"name": "staging", "ssh_host": "10.8.0.4", "ssh_key": "/k"},
				{"name": "home-lab", "ssh_host": "nas.local", "ssh_key": "/k"}
			]
		}`)}
		app.reload()
		if setup != nil {
			setup(app)
		}

		tt := ui.NewTesterWith(app.view, w, h, opts)
		path := filepath.Join(dir, name+".png")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s", path)
	}

	shot("light-idle", 600, 480, ui.TesterOptions{}, nil)
	shot("dark-idle", 600, 480, ui.TesterOptions{Dark: true}, nil)

	shot("light-mixed", 600, 480, ui.TesterOptions{}, func(app *guiApp) {
		connectFake(app.items[0])
		app.items[1].phase = phaseConnecting
		app.items[1].status = "正在获取登录地址"
		app.items[2].phase = phaseError
		app.items[2].status = "无法连接，3 秒后重试"
		app.items[2].detail = "dial tcp 192.168.1.200:22: connect: connection refused"
	})

	shot("light-expanded", 600, 480, ui.TesterOptions{}, func(app *guiApp) {
		connectFake(app.items[0])
		app.items[1].phase = phaseError
		app.items[1].status = "无法连接，3 秒后重试"
		app.items[1].detail = "dial tcp 10.8.0.4:22: connect: connection refused"
		app.items[1].showDetail = true
	})

	shot("dark-mixed", 600, 480, ui.TesterOptions{Dark: true}, func(app *guiApp) {
		connectFake(app.items[0])
		app.items[1].phase = phaseError
		app.items[1].status = "无法连接，3 秒后重试"
		app.items[1].detail = "dial tcp 10.8.0.4:22: i/o timeout"
		app.items[1].showDetail = true
	})

	shot("light-error", 600, 480, ui.TesterOptions{}, func(app *guiApp) {
		app.err = fmt.Errorf("reading config file: open /home/me/.claude-science/config.json: no such file or directory")
		app.items = nil
	})
	shot("light-error-open", 600, 480, ui.TesterOptions{}, func(app *guiApp) {
		app.err = fmt.Errorf("reading config file: open /home/me/.claude-science/config.json: no such file or directory")
		app.items = nil
		app.showErr = true
	})
	shot("light-empty", 600, 480, ui.TesterOptions{}, func(app *guiApp) {
		app.items = nil
	})

	shot("narrow", 460, 420, ui.TesterOptions{}, func(app *guiApp) {
		app.items[0].target.Name = "a-very-long-production-server-name"
		connectFake(app.items[0])
	})
	shot("wide", 880, 400, ui.TesterOptions{}, nil)
	shot("large-text", 620, 520, ui.TesterOptions{
		Preferences: ui.Preferences{TextScale: 1.35},
	}, func(app *guiApp) { connectFake(app.items[0]) })
}

// connectFake makes a card look connected, with a login URL, without opening
// a tunnel.
func connectFake(it *targetUI) {
	s := newSession(it.target)
	s.setURL("http://127.0.0.1:9981/?nonce=abc123")
	it.sess = s
	it.phase = phaseConnected
	it.status = "已连接"
}
