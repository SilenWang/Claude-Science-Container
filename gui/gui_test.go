package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/SilenWang/Claude-Science-Container/forward"
	"github.com/SilenWang/Claude-Science-Container/local"
)

// writeTempConfig writes body as the app's config.json and returns its path.
func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func phaseFromString(s string) forward.Phase { return forward.Phase(s) }
func phaseLocal(s string) local.Phase        { return local.Phase(s) }

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

const twoServers = `{
	"forward": {"targets": [
		{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_user": "root", "ssh_key": "/k"},
		{"name": "beta", "ssh_host": "10.0.0.2", "ssh_user": "deploy", "ssh_key": "/k"}
	]}
}`

func TestForwardTabShowsOneCardPerServer(t *testing.T) {
	app := &App{configPath: writeTempConfig(t, twoServers)}
	app.reload()

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	for _, label := range []string{"alpha", "root@10.0.0.1", "beta", "deploy@10.0.0.2"} {
		if !tt.HasText(label) {
			t.Fatalf("card text %q missing; texts: %v", label, tt.Texts())
		}
	}
	if !hasLabel(tt, "连接") {
		t.Fatalf("connect action missing; texts: %v", tt.Texts())
	}
	if !tt.HasText("添加服务器") {
		t.Fatalf("add action missing; texts: %v", tt.Texts())
	}
}

// The window is about servers: neither the config path nor the key paths,
// ports or commands of a target may show until the card is opened.
func TestForwardTabKeepsTechnicalFieldsOutOfSight(t *testing.T) {
	path := writeTempConfig(t, `{
		"forward": {"targets": [{
			"name": "alpha", "ssh_host": "10.0.0.1", "ssh_port": 2222,
			"ssh_key": "/secret/id_rsa", "container_id": "cs-1",
			"command": "claude-science url --verbose"
		}]}
	}`)

	app := &App{configPath: path}
	app.reload()

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	for _, leak := range []string{path, "config.json", "/secret/id_rsa", "cs-1", "--verbose", "2222"} {
		if tt.HasText(leak) {
			t.Fatalf("%q shows in the window; texts: %v", leak, tt.Texts())
		}
	}
}

func TestForwardTabEmptyState(t *testing.T) {
	app := &App{configPath: writeTempConfig(t, `{"forward":{"targets":[]}}`)}
	app.reload()

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	if !tt.HasText("还没有服务器") {
		t.Fatalf("empty state missing; texts: %v", tt.Texts())
	}
	if !hasLabel(tt, "添加服务器") {
		t.Fatalf("add action missing; texts: %v", tt.Texts())
	}
}

func TestBrokenConfigState(t *testing.T) {
	app := &App{configPath: writeTempConfig(t, `{ this is not json`)}
	app.reload()
	if app.err == nil {
		t.Fatal("broken config was accepted")
	}

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	if !tt.HasText("无法读取配置") {
		t.Fatalf("error state missing; texts: %v", tt.Texts())
	}
	if !hasLabel(tt, "重试") || !tt.HasText("打开配置文件夹") {
		t.Fatalf("recovery actions missing; texts: %v", tt.Texts())
	}
	for _, leak := range []string{"parsing config", "invalid character"} {
		if tt.HasText(leak) {
			t.Fatalf("raw error %q shows before it is asked for; texts: %v", leak, tt.Texts())
		}
	}
}

func TestLocalTabShowsItsSettings(t *testing.T) {
	app := &App{configPath: writeTempConfig(t, `{"forward":{"targets":[]}}`)}
	app.reload()
	app.tab = 1

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	for _, label := range []string{"接口后端", "API Key", "Base URL", "强制模型", "代理端口", "可执行文件路径", "Web 端口"} {
		if !tt.HasText(label) {
			t.Fatalf("field %q missing; texts: %v", label, tt.Texts())
		}
	}
	if !hasLabel(tt, "启动") {
		t.Fatalf("start action missing; texts: %v", tt.Texts())
	}
}

func TestLocalTabRunningState(t *testing.T) {
	app := &App{configPath: writeTempConfig(t, `{"forward":{"targets":[]}}`)}
	app.reload()
	app.tab = 1
	app.local.phase = local.PhaseReady
	app.local.url = "http://127.0.0.1:9981/?nonce=abc"

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	if !tt.HasText("运行中") {
		t.Fatalf("running state missing; texts: %v", tt.Texts())
	}
	if !tt.HasText("http://127.0.0.1:9981/?nonce=abc") {
		t.Fatalf("login URL missing; texts: %v", tt.Texts())
	}
	if !hasLabel(tt, "停止") {
		t.Fatalf("stop action missing; texts: %v", tt.Texts())
	}
}

// A failed connection keeps its technical text folded away until asked for.
func TestCardFoldsFailureDetail(t *testing.T) {
	app := &App{configPath: writeTempConfig(t, twoServers)}
	app.reload()

	item := app.targets[0]
	item.phase = forward.PhaseError
	item.detail = "SSH connection failed, retry in 3s: connection refused"

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	if tt.HasText("connection refused") {
		t.Fatalf("failure detail shows unfolded; texts: %v", tt.Texts())
	}
	if !hasLabel(tt, "详细信息") {
		t.Fatalf("detail toggle missing; texts: %v", tt.Texts())
	}
	if err := tt.Click("详细信息"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("connection refused") {
		t.Fatalf("detail missing after expanding; texts: %v", tt.Texts())
	}
}

// Both tabs are tabs of one window: choosing the second one shows its body,
// and choosing the first brings the servers back.
func TestTabsSwitchBothWays(t *testing.T) {
	app := &App{configPath: writeTempConfig(t, twoServers)}
	app.reload()

	tt := ui.NewTesterWith(app.view, 760, 640, ui.TesterOptions{})
	if !tt.HasText("alpha") {
		t.Fatalf("forward tab not shown first; texts: %v", tt.Texts())
	}
	if err := tt.Click("启动本地的 Claude Science"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("接口后端") {
		t.Fatalf("local tab did not open; texts: %v", tt.Texts())
	}
	if strings.Contains(strings.Join(tt.Texts(), "\n"), "alpha") {
		t.Fatalf("forward content stayed under the local tab; texts: %v", tt.Texts())
	}
	if err := tt.Click("转发"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("alpha") {
		t.Fatalf("forward tab did not come back; texts: %v", tt.Texts())
	}
}
