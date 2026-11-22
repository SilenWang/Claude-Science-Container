package local

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SilenWang/Claude-Science-Container/config"
	"github.com/SilenWang/Claude-Science-Container/proxy"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testKey() string {
	return base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
}

func writeKey(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".claude-science")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "encryption.key"), []byte("OAUTH_ENCRYPTION_KEY="+testKey()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

// writeFakeClaudeScience writes a POSIX shell stand-in that records its args,
// optionally creates encryption.key, and prints a login URL.
func writeFakeClaudeScience(t *testing.T, home string, createKey bool, printURL bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude-science uses a POSIX shell script")
	}
	script := `#!/bin/sh
echo "cs $*" >> "` + filepath.Join(home, "cs.log") + `"
if [ "$1" = "serve" ]; then
  key="$HOME/.claude-science/encryption.key"
  if [ ! -f "$key" ]; then
    :
`
	if createKey {
		script += `    mkdir -p "$HOME/.claude-science"
    printf 'OAUTH_ENCRYPTION_KEY=%s\n' '` + testKey() + `' > "$key"
`
	}
	script += `  fi
  if [ "$2" = "--no-browser" ] && [ "$3" = "--detached" ]; then
    exit 0
  fi
`
	if printURL {
		script += `  echo "ANTHROPIC_BASE_URL=$ANTHROPIC_BASE_URL"
  echo "Open http://127.0.0.1:9981/?token=fake"
`
	}
	script += `  sleep 60
fi
if [ "$1" = "stop" ]; then
  exit 0
fi
`

	path := filepath.Join(home, "fake-claude-science")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude-science: %v", err)
	}
	return path
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func baseConfig(t *testing.T, csPath string, proxyPort int) config.Local {
	t.Helper()
	cfg := config.Default().Local
	cfg.ClaudeSciencePath = csPath
	cfg.ProxyPort = proxyPort
	cfg.ClaudeSciencePort = freePort(t)
	cfg.Backends["deepseek"] = config.Backend{
		APIKey:       "ds-test-key",
		BaseURL:      "https://api.deepseek.com/v1",
		UpstreamMode: "openai",
	}
	return cfg
}

func TestRunnerStartsStack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude-science uses a POSIX shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeKey(t, home)
	csPath := writeFakeClaudeScience(t, home, false, true)

	var stdout, stderr syncBuffer
	var opened []string
	var proxyMu sync.Mutex
	var proxyOpts proxy.Options
	proxyStarted := make(chan struct{}, 1)

	cfg := baseConfig(t, csPath, freePort(t))
	runner := New(Options{
		Config:          cfg,
		Stdout:          &stdout,
		Stderr:          &stderr,
		AutoOpenBrowser: true,
		OpenBrowser: func(url string) error {
			opened = append(opened, url)
			return nil
		},
		StartProxy: func(ctx context.Context, opts proxy.Options, version string, w io.Writer) error {
			proxyMu.Lock()
			proxyOpts = opts
			proxyMu.Unlock()
			proxyStarted <- struct{}{}
			<-ctx.Done()
			return nil
		},
		WaitProxy: func(ctx context.Context, addr string, timeout time.Duration) error { return nil },
		URLWait:   5 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Start: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if got := runner.URL(); got != "http://127.0.0.1:9981/?token=fake" {
		t.Fatalf("URL() = %q\nstdout:\n%s", got, stdout.String())
	}
	if len(opened) != 1 || opened[0] != runner.URL() {
		t.Fatalf("opened = %#v", opened)
	}

	// The fake prints the env it received; it must point at the in-process proxy.
	wantBase := fmt.Sprintf("ANTHROPIC_BASE_URL=http://127.0.0.1:%d", cfg.ProxyPort)
	if !strings.Contains(stdout.String(), wantBase) {
		t.Fatalf("stdout missing %q:\n%s", wantBase, stdout.String())
	}

	// The fake OAuth token must have been written under the temp HOME.
	tokenPath := filepath.Join(home, ".claude-science", ".oauth-tokens", "byok-user-000000000000000000.enc")
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("token not written: %v", err)
	}

	proxyMu.Lock()
	gotOpts := proxyOpts
	proxyMu.Unlock()
	if gotOpts.DefaultBackend != "deepseek" || gotOpts.ForceModel != "deepseek-flash" {
		t.Fatalf("proxy options = %+v", gotOpts)
	}
	if gotOpts.ProxyPort != cfg.ProxyPort || gotOpts.ProxyHost != "127.0.0.1" {
		t.Fatalf("proxy listen = %s:%d", gotOpts.ProxyHost, gotOpts.ProxyPort)
	}
	if len(gotOpts.Backends) != 3 || gotOpts.Backends[0].APIKey != "ds-test-key" {
		t.Fatalf("proxy backends = %+v", gotOpts.Backends)
	}

	runner.Stop()
	if err := runner.Wait(); err != nil {
		t.Fatalf("Wait after Stop: %v", err)
	}
	if runner.Status().Phase != PhaseStopped {
		t.Fatalf("final status = %+v", runner.Status())
	}
}

func TestRunnerCreatesEncryptionKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude-science uses a POSIX shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	csPath := writeFakeClaudeScience(t, home, true, true)

	cfg := baseConfig(t, csPath, freePort(t))
	runner := New(Options{
		Config: cfg,
		Stdout: &syncBuffer{},
		Stderr: &syncBuffer{},
		StartProxy: func(ctx context.Context, opts proxy.Options, version string, w io.Writer) error {
			<-ctx.Done()
			return nil
		},
		WaitProxy: func(ctx context.Context, addr string, timeout time.Duration) error { return nil },
		URLWait:   5 * time.Second,
		KeyWait:   5 * time.Second,
	})

	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runner.Stop()

	keyPath := filepath.Join(home, ".claude-science", "encryption.key")
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("encryption.key was not created: %v", err)
	}
	tokenPath := filepath.Join(home, ".claude-science", ".oauth-tokens", "byok-user-000000000000000000.enc")
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("token was not written after key creation: %v", err)
	}

	log, err := os.ReadFile(filepath.Join(home, "cs.log"))
	if err != nil {
		t.Fatalf("read cs.log: %v", err)
	}
	if !strings.Contains(string(log), "serve --no-browser --detached --port") {
		t.Fatalf("claude-science was not started detached:\n%s", log)
	}
	if !strings.Contains(string(log), "cs stop") {
		t.Fatalf("claude-science stop was not called:\n%s", log)
	}
}

func TestRunnerFailsWithoutAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"DEEPSEEK_API_KEY", "OPENAI_API_KEY", "CUSTOM_API_KEY"} {
		t.Setenv(key, "")
	}
	writeKey(t, home)

	cfg := config.Default().Local
	cfg.ClaudeSciencePath = filepath.Join(home, "missing-binary")
	cfg.Backends["deepseek"] = config.Backend{BaseURL: "https://api.deepseek.com/v1"}

	runner := New(Options{Config: cfg, Stdout: &syncBuffer{}, Stderr: &syncBuffer{}})
	err := runner.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded without an API key")
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("error = %v", err)
	}
	if waitErr := runner.Wait(); waitErr == nil {
		t.Fatal("Wait returned nil after a failed Start")
	}
}

func TestRunnerReportsClaudeScienceExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude-science uses a POSIX shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeKey(t, home)
	csPath := filepath.Join(home, "dying-claude-science")
	if err := os.WriteFile(csPath, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cfg := baseConfig(t, csPath, freePort(t))
	var stdout syncBuffer
	runner := New(Options{
		Config: cfg,
		Stdout: &stdout,
		Stderr: &syncBuffer{},
		StartProxy: func(ctx context.Context, opts proxy.Options, version string, w io.Writer) error {
			<-ctx.Done()
			return nil
		},
		WaitProxy: func(ctx context.Context, addr string, timeout time.Duration) error { return nil },
		URLWait:   5 * time.Second,
	})

	err := runner.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded although claude-science exited")
	}
	if !strings.Contains(err.Error(), "claude-science exited") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunnerURLTimeoutStillRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude-science uses a POSIX shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeKey(t, home)
	csPath := writeFakeClaudeScience(t, home, false, false)

	cfg := baseConfig(t, csPath, freePort(t))
	runner := New(Options{
		Config: cfg,
		Stdout: &syncBuffer{},
		Stderr: &syncBuffer{},
		StartProxy: func(ctx context.Context, opts proxy.Options, version string, w io.Writer) error {
			<-ctx.Done()
			return nil
		},
		WaitProxy: func(ctx context.Context, addr string, timeout time.Duration) error { return nil },
		URLWait:   100 * time.Millisecond,
	})
	if err := runner.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if runner.URL() != "" {
		t.Fatalf("URL = %q, want empty", runner.URL())
	}
	if runner.Status().Phase != PhaseReady {
		t.Fatalf("status = %+v", runner.Status())
	}
	runner.Stop()
	_ = runner.Wait()
}

func TestProxyOptionsMapping(t *testing.T) {
	l := config.Default().Local
	l.DefaultBackend = "custom"
	l.ForceModel = "custom-model"
	l.ModelListMode = "aliases"
	l.InlineImagePolicy = "preserve"
	l.ProxyHost = "0.0.0.0"
	l.ProxyPort = 12345
	l.ModelAliases = []config.ModelAlias{{ID: "claude-sonnet-4-5", DisplayName: "Mine", Backend: "custom", Model: "m1"}}
	l.Backends["custom"] = config.Backend{APIKey: "k", BaseURL: "https://example.test/v1", UpstreamMode: "openai", ModelPattern: "^m"}

	opts := ProxyOptions(l)
	if opts.DefaultBackend != "custom" || opts.ForceModel != "custom-model" {
		t.Fatalf("options = %+v", opts)
	}
	if opts.ProxyHost != "127.0.0.1" || opts.ProxyPort != 12345 {
		t.Fatalf("listen = %s:%d", opts.ProxyHost, opts.ProxyPort)
	}
	if len(opts.ModelAliases) != 1 || opts.ModelAliases[0].Model != "m1" {
		t.Fatalf("aliases = %+v", opts.ModelAliases)
	}
	if len(opts.Backends) != 3 {
		t.Fatalf("backends = %+v", opts.Backends)
	}
	found := false
	for _, b := range opts.Backends {
		if b.Name == "custom" && b.APIKey == "k" && b.ModelPattern == "^m" {
			found = true
		}
	}
	if !found {
		t.Fatalf("custom backend not mapped: %+v", opts.Backends)
	}
}
