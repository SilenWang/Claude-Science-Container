package gui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestScreenshots renders both tabs in every state they can be in, as PNGs,
// so the design can be reviewed without a desktop. It runs with the rest of
// the suite; -v lists where the files are. Set SHOTS_DIR to keep them
// somewhere findable.
func TestScreenshots(t *testing.T) {
	dir := os.Getenv("SHOTS_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	shot := func(name string, w, h int, opts ui.TesterOptions, setup func(*App)) {
		t.Helper()

		app := &App{configPath: writeTempConfig(t, `{
			"forward": {"targets": []},
			"local": {"default_backend": "deepseek",
				"backends": {"deepseek": {"api_key": "sk-••••••••", "base_url": "https://api.deepseek.com/v1"}}}
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

	servers := `{
		"forward": {"targets": [
			{"name": "production", "ssh_host": "192.168.1.100", "ssh_user": "root", "ssh_key": "/k",
			 "container_id": "cs-prod", "command": "claude-science url",
			 "port_forwards": [{"local": 9876, "remote": 9876}, {"local": 9981, "remote": 9981}]},
			{"name": "staging", "ssh_host": "10.8.0.4", "ssh_user": "deploy", "ssh_key": "/k"},
			{"name": "home-lab", "ssh_host": "nas.local", "ssh_user": "admin", "ssh_key": "/k"}
		]},
		"local": {"default_backend": "deepseek",
			"backends": {"deepseek": {"api_key": "sk-••••••••", "base_url": "https://api.deepseek.com/v1"}}}
	}`
	withServers := func(setup func(*App)) func(*App) {
		return func(app *App) {
			app.configPath = writeTempConfig(t, servers)
			app.reload()
			if setup != nil {
				setup(app)
			}
		}
	}

	shot("forward-empty", 760, 640, ui.TesterOptions{}, func(app *App) {
		app.tab = 0
	})
	shot("forward-first-run", 760, 640, ui.TesterOptions{}, func(app *App) {
		app.configPath = filepath.Join(t.TempDir(), "config.json")
		app.reload()
		app.tab = 0
		app.targets[0].expanded = true
	})
	shot("forward-idle", 760, 640, ui.TesterOptions{}, withServers(func(app *App) {
		app.tab = 0
		app.targets[1].expanded = true
	}))
	shot("forward-connected", 760, 640, ui.TesterOptions{}, withServers(func(app *App) {
		app.tab = 0
		app.targets[0].phase = phaseFromString("connected")
		app.targets[0].url = "http://127.0.0.1:9981/?nonce=8f2a41"
		app.targets[1].phase = phaseFromString("connecting")
		app.targets[2].phase = phaseFromString("error")
		app.targets[2].detail = "SSH connection failed, retry in 3s: dial tcp 192.168.1.200:22: connect: connection refused"
	}))
	shot("forward-error-open", 760, 640, ui.TesterOptions{}, withServers(func(app *App) {
		app.tab = 0
		app.targets[0].phase = phaseFromString("error")
		app.targets[0].detail = "SSH connection failed, retry in 3s: ssh: handshake failed: knownhosts: key mismatch"
		app.targets[0].showDetail = true
	}))
	shot("forward-empty-dark", 760, 640, ui.TesterOptions{Dark: true}, func(app *App) {
		app.tab = 0
	})

	shot("local-idle", 760, 640, ui.TesterOptions{}, func(app *App) {
		app.tab = 1
	})
	shot("local-running", 760, 640, ui.TesterOptions{}, func(app *App) {
		app.tab = 1
		app.local.phase = phaseLocal("ready")
		app.local.url = "http://127.0.0.1:9981/?nonce=8f2a41"
	})
	shot("local-error", 760, 640, ui.TesterOptions{}, func(app *App) {
		app.tab = 1
		app.local.phase = phaseLocal("error")
		app.local.detail = "backend \"deepseek\" has no API key: run `local --set-key <KEY> --backend deepseek` (or edit config.json)"
		app.bannerText = "启动失败"
		app.bannerDetail = app.local.detail
	})
	shot("local-running-dark", 760, 640, ui.TesterOptions{Dark: true}, func(app *App) {
		app.tab = 1
		app.local.phase = phaseLocal("ready")
		app.local.url = "http://127.0.0.1:9981/?nonce=8f2a41"
	})

	shot("broken-config", 760, 640, ui.TesterOptions{}, func(app *App) {
		app.configPath = writeTempConfig(t, `{ "forward": { "targets": [`)
		app.reload()
		app.tab = 0
	})
	shot("broken-config-open", 760, 640, ui.TesterOptions{}, func(app *App) {
		app.configPath = writeTempConfig(t, `{ "forward": { "targets": [`)
		app.reload()
		app.showErr = true
	})

	longName := `{
		"forward": {"targets": [
			{"name": "a-very-long-production-server-name", "ssh_host": "192.168.1.100", "ssh_user": "root", "ssh_key": "/k",
			 "port_forwards": [{"local": 9876, "remote": 9876}, {"local": 9981, "remote": 9981}]},
			{"name": "staging", "ssh_host": "10.8.0.4", "ssh_user": "deploy", "ssh_key": "/k"},
			{"name": "home-lab", "ssh_host": "nas.local", "ssh_user": "admin", "ssh_key": "/k"}
		]},
		"local": {"default_backend": "deepseek",
			"backends": {"deepseek": {"api_key": "sk-••••••••", "base_url": "https://api.deepseek.com/v1"}}}
	}`
	shot("narrow-connected", 560, 520, ui.TesterOptions{}, func(app *App) {
		app.configPath = writeTempConfig(t, longName)
		app.reload()
		app.tab = 0
		app.targets[0].phase = phaseFromString("connected")
		app.targets[0].url = "http://127.0.0.1:9981/?nonce=8f2a41"
	})
	shot("large-text", 760, 700, ui.TesterOptions{
		Preferences: ui.Preferences{TextScale: 1.3},
	}, withServers(func(app *App) {
		app.tab = 1
	}))
}
