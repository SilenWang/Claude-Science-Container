package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestViewShowsOneCardPerTarget(t *testing.T) {
	path := writeTempConfig(t, `{
		"targets": [
			{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"},
			{"name": "beta", "ssh_host": "10.0.0.2", "ssh_key": "/k"}
		]
	}`)

	app := &guiApp{configPath: path}
	app.reload()

	tt := ui.NewTester(app.view, 600, 480)
	for _, label := range []string{"alpha", "root@10.0.0.1", "beta", "root@10.0.0.2"} {
		if !tt.HasText(label) {
			t.Fatalf("card text %q missing; texts: %v", label, tt.Texts())
		}
	}
	if !tt.HasText("连接") {
		t.Fatalf("connect action missing; texts: %v", tt.Texts())
	}
}

// The window is about servers: neither the config path nor the key paths,
// ports or commands of a target may show in it.
func TestViewKeepsConfigOutOfSight(t *testing.T) {
	path := writeTempConfig(t, `{
		"targets": [
			{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/secret/id_rsa",
			 "container_id": "cs-1", "command": "claude-science url"}
		]
	}`)

	app := &guiApp{configPath: path}
	app.reload()

	tt := ui.NewTester(app.view, 600, 480)
	for _, leak := range []string{path, "config.json", "/secret/id_rsa", "cs-1", "claude-science url"} {
		if tt.HasText(leak) {
			t.Fatalf("%q shows in the window; texts: %v", leak, tt.Texts())
		}
	}
}

func TestViewReportsConfigError(t *testing.T) {
	app := &guiApp{configPath: filepath.Join(t.TempDir(), "config.json")}
	app.reload()

	tt := ui.NewTester(app.view, 600, 480)
	if !tt.HasText("无法读取配置") {
		t.Fatalf("error missing; texts: %v", tt.Texts())
	}
	// The reader gets a sentence and the ways out, not a stack of paths.
	if !tt.HasText("重试") || !tt.HasText("打开配置文件夹") {
		t.Fatalf("recovery actions missing; texts: %v", tt.Texts())
	}
	for _, leak := range []string{"reading config file", "no such file"} {
		if tt.HasText(leak) {
			t.Fatalf("raw error %q shows before it is asked for; texts: %v", leak, tt.Texts())
		}
	}
}

func TestViewReportsNoTargets(t *testing.T) {
	path := writeTempConfig(t, `{"targets": []}`)

	app := &guiApp{configPath: path}
	app.reload()
	if app.err != nil {
		t.Fatalf("an empty list is a valid config: %v", app.err)
	}

	tt := ui.NewTester(app.view, 600, 480)
	if !tt.HasText("还没有服务器") {
		t.Fatalf("empty state missing; texts: %v", tt.Texts())
	}
	if !tt.HasText("重新加载") {
		t.Fatalf("recovery action missing; texts: %v", tt.Texts())
	}
}

// A failed connection keeps its technical text folded away until asked for.
func TestCardFoldsFailureDetail(t *testing.T) {
	app := &guiApp{configPath: writeTempConfig(t, `{
		"targets": [{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"}]
	}`)}
	app.reload()

	it := app.items[0]
	it.phase = phaseError
	it.status = "无法连接，3 秒后重试"
	it.detail = "TCP dial 10.0.0.1:22: connect: connection refused"

	tt := ui.NewTester(app.view, 600, 480)
	if tt.HasText(it.detail) {
		t.Fatalf("failure detail shows unfolded; texts: %v", tt.Texts())
	}
	if !tt.HasText("详细信息") {
		t.Fatalf("detail toggle missing; texts: %v", tt.Texts())
	}
	if err := tt.Click("详细信息"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, text := range tt.Texts() {
		if strings.Contains(text, "connection refused") {
			found = true
		}
	}
	if !found {
		t.Fatalf("detail missing after expanding; texts: %v", tt.Texts())
	}
}

func TestParseURL(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   string
	}{
		{`Login at http://127.0.0.1:9981/?token=abc`, "http://127.0.0.1:9981/?token=abc"},
		{`{"url":"https://example.test/x"}`, "https://example.test/x"},
		{"no url here", ""},
	} {
		if got := parseURL(tc.output); got != tc.want {
			t.Errorf("parseURL(%q) = %q, want %q", tc.output, got, tc.want)
		}
	}
}

func TestDisplayNameAndAddress(t *testing.T) {
	named := Target{Name: "production", SSHHost: "10.0.0.1", SSHUser: "root"}
	if got := displayName(named); got != "production" {
		t.Errorf("displayName = %q", got)
	}
	if got := address(named); got != "root@10.0.0.1" {
		t.Errorf("address = %q", got)
	}

	anon := Target{SSHHost: "10.0.0.2"}
	if got := displayName(anon); got != "10.0.0.2" {
		t.Errorf("displayName without a name = %q", got)
	}
	if got := address(Target{SSHHost: "10.0.0.3"}); got != "10.0.0.3" {
		t.Errorf("address without a user = %q", got)
	}
}
