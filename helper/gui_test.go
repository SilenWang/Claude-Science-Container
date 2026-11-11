package main

import (
	"os"
	"path/filepath"
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

func TestViewShowsOneButtonPerTarget(t *testing.T) {
	path := writeTempConfig(t, `{
		"targets": [
			{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"},
			{"name": "beta", "ssh_host": "10.0.0.2", "ssh_key": "/k"}
		]
	}`)

	app := &guiApp{configPath: path}
	app.reload()

	tt := ui.NewTester(app.view, 560, 460)
	for _, label := range []string{"alpha", "beta"} {
		if !tt.HasText(label) {
			t.Fatalf("button %q missing; texts: %v", label, tt.Texts())
		}
	}
	if !tt.HasText("未启动") {
		t.Fatalf("status missing; texts: %v", tt.Texts())
	}
}

func TestViewReportsConfigError(t *testing.T) {
	app := &guiApp{configPath: filepath.Join(t.TempDir(), "config.json")}
	app.reload()

	tt := ui.NewTester(app.view, 560, 460)
	if !tt.HasText("读取配置失败") {
		t.Fatalf("error missing; texts: %v", tt.Texts())
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
