// Package local runs the whole local stack: it makes sure claude-science has
// an encryption key and a fake OAuth token, starts the Anthropic → OpenAI
// proxy in-process, then launches claude-science against that proxy and
// surfaces the login URL.
package local

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SilenWang/Claude-Science-Container/config"
	"github.com/SilenWang/Claude-Science-Container/forward"
	"github.com/SilenWang/Claude-Science-Container/proxy"
)

// Phase is the coarse state of the local stack.
type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhasePreparing   Phase = "preparing"
	PhaseCredentials Phase = "credentials"
	PhaseProxy       Phase = "proxy"
	PhaseStarting    Phase = "starting"
	PhaseReady       Phase = "ready"
	PhaseError       Phase = "error"
	PhaseStopped     Phase = "stopped"
)

// Status is one state change delivered to a Reporter.
type Status struct {
	Phase   Phase  `json:"phase"`
	Message string `json:"message,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Reporter receives every state change. Do not block.
type Reporter func(Status)

// Options configures a Runner. Zero values use the production implementations.
type Options struct {
	Config          config.Local
	Version         string
	Stdout          io.Writer
	Stderr          io.Writer
	AutoOpenBrowser bool
	Reporter        Reporter

	NewCommand  func(ctx context.Context, name string, args ...string) *exec.Cmd
	StartProxy  func(ctx context.Context, opts proxy.Options, version string, stderr io.Writer) error
	SetupToken  func(w io.Writer) error
	HomeDir     func() (string, error)
	LookPath    func(string) (string, error)
	OpenBrowser func(string) error
	WaitProxy   func(ctx context.Context, addr string, timeout time.Duration) error

	KeyWait   time.Duration
	ProxyWait time.Duration
	URLWait   time.Duration
	StopWait  time.Duration
}

func (o Options) withDefaults() Options {
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.NewCommand == nil {
		o.NewCommand = exec.CommandContext
	}
	if o.StartProxy == nil {
		o.StartProxy = func(ctx context.Context, opts proxy.Options, version string, stderr io.Writer) error {
			return proxy.ServeContext(ctx, opts, version, stderr)
		}
	}
	if o.SetupToken == nil {
		o.SetupToken = proxy.SetupToken
	}
	if o.HomeDir == nil {
		o.HomeDir = os.UserHomeDir
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.OpenBrowser == nil {
		o.OpenBrowser = forward.OpenInBrowser
	}
	if o.WaitProxy == nil {
		o.WaitProxy = waitForTCP
	}
	if o.Version == "" {
		o.Version = proxy.DefaultVersion
	}
	if o.KeyWait == 0 {
		o.KeyWait = 20 * time.Second
	}
	if o.ProxyWait == 0 {
		o.ProxyWait = 10 * time.Second
	}
	if o.URLWait == 0 {
		o.URLWait = 30 * time.Second
	}
	if o.StopWait == 0 {
		o.StopWait = 5 * time.Second
	}
	return o
}

// Runner owns one local stack. Start it once; Stop and Wait to shut down.
type Runner struct {
	opts Options

	mu       sync.Mutex
	started  bool
	status   Status
	url      string
	err      error
	csPath   string
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	stopCh   chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	doneOnce sync.Once
}

// New builds a Runner; the config is defaulted on a copy.
func New(opts Options) *Runner {
	opts = opts.withDefaults()
	opts.Config.ApplyDefaults()
	return &Runner{
		opts:   opts,
		status: Status{Phase: PhaseIdle},
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start brings the stack up. It returns once claude-science printed the login
// URL (or the URL wait elapsed), leaving the process running.
func (r *Runner) Start(parent context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return fmt.Errorf("local runner already started")
	}
	r.started = true
	r.mu.Unlock()

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel

	if err := r.prepare(); err != nil {
		cancel()
		r.report(PhaseError, err.Error(), "")
		r.fail(err)
		return err
	}

	// `claude-science serve` attaches to a daemon that is already running
	// instead of starting a fresh one, and that daemon keeps the environment it
	// was started with. A daemon left behind by a manual start (or by the
	// container) has no ANTHROPIC_BASE_URL, so its requests bypass the bridge
	// and Claude Science reports "Your Claude session is no longer valid".
	// Stop it so the daemon we start below picks up the bridge address.
	r.report(PhasePreparing, "stopping any claude-science daemon left running", "")
	if err := r.stopDaemon(ctx); err != nil {
		fmt.Fprintf(r.opts.Stderr, "warning: %v\n", err)
	}

	addr := ProxyAddress(r.opts.Config)
	proxyErr := make(chan error, 1)
	r.report(PhaseProxy, "starting local API bridge on "+addr, "")
	go func() {
		proxyErr <- r.opts.StartProxy(ctx, r.ProxyOptions(), r.opts.Version, r.opts.Stderr)
	}()

	if err := r.opts.WaitProxy(ctx, addr, r.opts.ProxyWait); err != nil {
		cancel()
		r.report(PhaseError, err.Error(), "")
		r.fail(err)
		return err
	}
	r.report(PhaseProxy, "local API bridge listening on http://"+addr, "")

	baseURL := "http://" + addr
	cmd := r.opts.NewCommand(ctx, r.csPath,
		"serve", "--no-browser",
		"--host", loopbackHost(r.opts.Config.ProxyHost),
		"--port", strconv.Itoa(r.opts.Config.ClaudeSciencePort),
	)
	cmd.Env = append(os.Environ(), "ANTHROPIC_BASE_URL="+baseURL)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		err = fmt.Errorf("claude-science stdout: %w", err)
		r.report(PhaseError, err.Error(), "")
		r.fail(err)
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		err = fmt.Errorf("claude-science stderr: %w", err)
		r.report(PhaseError, err.Error(), "")
		r.fail(err)
		return err
	}

	r.report(PhaseStarting, fmt.Sprintf("starting %s serve --no-browser --port %d (ANTHROPIC_BASE_URL=%s)",
		r.csPath, r.opts.Config.ClaudeSciencePort, baseURL), "")
	if err := cmd.Start(); err != nil {
		cancel()
		err = fmt.Errorf("starting claude-science: %w", err)
		r.report(PhaseError, err.Error(), "")
		r.fail(err)
		return err
	}
	r.mu.Lock()
	r.cmd = cmd
	r.mu.Unlock()

	urlCh := make(chan string, 1)
	var urlOnce sync.Once
	go r.scanOutput(stdout, urlCh, &urlOnce)
	go r.scanOutput(stderr, urlCh, &urlOnce)

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	select {
	case url := <-urlCh:
		r.setURL(url)
		r.report(PhaseReady, "claude-science ready at "+url, url)
		if r.opts.AutoOpenBrowser {
			if err := r.opts.OpenBrowser(url); err != nil {
				fmt.Fprintf(r.opts.Stderr, "warning: could not open browser: %v\n", err)
			}
		}
		go r.reap(exitCh)
		return nil
	case err := <-exitCh:
		if r.stopRequested() {
			r.finish(nil)
			return nil
		}
		if err == nil {
			err = errors.New("process exited")
		}
		r.report(PhaseError, fmt.Sprintf("claude-science exited: %v", err), "")
		r.finish(err)
		return fmt.Errorf("claude-science exited: %w", err)
	case err := <-proxyErr:
		if r.stopRequested() {
			r.finish(nil)
			return nil
		}
		cancel()
		r.report(PhaseError, fmt.Sprintf("local API bridge exited: %v", err), "")
		r.finish(err)
		return fmt.Errorf("local API bridge exited: %w", err)
	case <-time.After(r.opts.URLWait):
		r.report(PhaseReady, fmt.Sprintf("claude-science is running (no login URL after %s; check the output above)", r.opts.URLWait), "")
		go r.reap(exitCh)
		return nil
	case <-ctx.Done():
		r.finish(nil)
		return ctx.Err()
	}
}

// prepare validates the config, creates the encryption key when missing, and
// writes the fake OAuth token.
func (r *Runner) prepare() error {
	r.report(PhasePreparing, "checking local configuration", "")

	b, ok := r.opts.Config.EffectiveBackend(r.opts.Config.DefaultBackend)
	if !ok {
		return fmt.Errorf("unknown default backend %q: use deepseek, openai, or custom", r.opts.Config.DefaultBackend)
	}
	if b.APIKey == "" {
		return fmt.Errorf("backend %q has no API key: run `local --set-key <KEY> --backend %s` (or edit config.json)",
			r.opts.Config.DefaultBackend, r.opts.Config.DefaultBackend)
	}
	if b.BaseURL == "" {
		return fmt.Errorf("backend %q has no base URL: run `local --set-base-url <URL> --backend %s` (or edit config.json)",
			r.opts.Config.DefaultBackend, r.opts.Config.DefaultBackend)
	}
	csPath, err := r.resolveClaudeScience()
	if err != nil {
		return err
	}
	r.csPath = csPath

	home, err := r.opts.HomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	keyPath := filepath.Join(home, ".claude-science", "encryption.key")
	if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		r.report(PhaseCredentials, "encryption.key missing; asking claude-science to create it", "")
		if err := r.createEncryptionKey(keyPath); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("checking %s: %w", keyPath, err)
	}

	r.report(PhaseCredentials, "writing fake OAuth token", "")
	if err := r.opts.SetupToken(r.opts.Stdout); err != nil {
		return fmt.Errorf("writing fake OAuth token: %w", err)
	}
	return nil
}

// stopDaemon stops a claude-science daemon left over from an earlier start. Its
// output is discarded: "nothing to stop" is the common case and not an error.
func (r *Runner) stopDaemon(ctx context.Context) error {
	stopCtx, cancel := context.WithTimeout(ctx, r.opts.StopWait)
	defer cancel()
	cmd := r.opts.NewCommand(stopCtx, r.csPath, "stop")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("stopping the claude-science daemon that was already running: %w (run `%s stop` and retry)", err, r.csPath)
	}
	return nil
}

// createEncryptionKey briefly runs claude-science detached (as the container
// entrypoint does) and waits for it to write encryption.key.
func (r *Runner) createEncryptionKey(keyPath string) error {
	port, err := reservePort()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.opts.KeyWait+10*time.Second)
	defer cancel()

	cmd := r.opts.NewCommand(ctx, r.csPath,
		"serve", "--no-browser", "--detached", "--port", strconv.Itoa(port))
	cmd.Stdout = r.opts.Stdout
	cmd.Stderr = r.opts.Stderr

	waitCh := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting claude-science to create encryption.key: %w", err)
	}
	go func() { waitCh <- cmd.Wait() }()

	deadline := time.Now().Add(r.opts.KeyWait)
	for {
		if _, err := os.Stat(keyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			return fmt.Errorf("claude-science did not create %s within %s", keyPath, r.opts.KeyWait)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Stop the detached instance the same way the container entrypoint does.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), r.opts.StopWait)
	stopCmd := r.opts.NewCommand(stopCtx, r.csPath, "stop")
	stopCmd.Stdout = io.Discard
	stopCmd.Stderr = io.Discard
	_ = stopCmd.Run()
	stopCancel()

	select {
	case <-waitCh:
	case <-time.After(r.opts.StopWait):
		cancel()
	}
	return nil
}

func (r *Runner) resolveClaudeScience() (string, error) {
	path := strings.TrimSpace(r.opts.Config.ClaudeSciencePath)
	if path == "" {
		path = "claude-science"
	}
	if strings.ContainsRune(path, os.PathSeparator) {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("claude-science binary not found at %s (set local.claude_science_path): %w", path, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("local.claude_science_path %s is a directory", path)
		}
		return path, nil
	}
	resolved, err := r.opts.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("claude-science binary %q not found in PATH: %w", path, err)
	}
	return resolved, nil
}

func (r *Runner) scanOutput(reader io.Reader, urlCh chan<- string, once *sync.Once) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(r.opts.Stdout, line)
		// Never mistake the proxy address (or an echoed env line) for the
		// claude-science login URL.
		if strings.HasPrefix(strings.TrimSpace(line), "ANTHROPIC_BASE_URL=") {
			continue
		}
		if url := forward.ParseLoginURL(line); url != "" {
			once.Do(func() { urlCh <- url })
		}
	}
}

// reap waits for claude-science to exit after Start has already returned.
func (r *Runner) reap(exitCh <-chan error) {
	err := <-exitCh
	if r.stopRequested() || errors.Is(err, context.Canceled) {
		err = nil
	}
	r.finish(err)
}

func (r *Runner) finish(exitErr error) {
	r.mu.Lock()
	if exitErr != nil && r.err == nil {
		r.err = exitErr
	}
	r.mu.Unlock()
	r.report(PhaseStopped, "stopped", r.URL())
	r.doneOnce.Do(func() { close(r.done) })
}

func (r *Runner) fail(err error) {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
	r.doneOnce.Do(func() { close(r.done) })
}

func (r *Runner) stopRequested() bool {
	select {
	case <-r.stopCh:
		return true
	default:
		return false
	}
}

// Stop shuts the stack down; it is safe to call more than once.
func (r *Runner) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		if r.cancel != nil {
			r.cancel()
		}
		r.mu.Lock()
		cmd := r.cmd
		r.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
}

// Wait blocks until claude-science exited (or Start failed).
func (r *Runner) Wait() error {
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// URL returns the login URL once seen.
func (r *Runner) URL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.url
}

// Status returns the last state change.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// ProxyOptions translates this runner's config into the bridge options.
func (r *Runner) ProxyOptions() proxy.Options {
	return ProxyOptions(r.opts.Config)
}

func (r *Runner) setURL(url string) {
	r.mu.Lock()
	r.url = url
	r.mu.Unlock()
}

func (r *Runner) report(phase Phase, message, url string) {
	status := Status{Phase: phase, Message: message, URL: url}
	r.mu.Lock()
	r.status = status
	reporter := r.opts.Reporter
	r.mu.Unlock()
	if message != "" {
		fmt.Fprintf(r.opts.Stdout, "[local] %s\n", message)
	}
	if reporter != nil {
		reporter(status)
	}
}

// ProxyOptions maps the unified config onto the bridge's options, applying the
// container environment variables only as a fallback for empty fields.
func ProxyOptions(l config.Local) proxy.Options {
	opts := proxy.Options{
		DefaultBackend:    l.DefaultBackend,
		ForceModel:        l.ForceModel,
		ModelListMode:     l.ModelListMode,
		InlineImagePolicy: l.InlineImagePolicy,
		ProxyHost:         loopbackHost(l.ProxyHost),
		ProxyPort:         l.ProxyPort,
	}
	for _, alias := range l.ModelAliases {
		opts.ModelAliases = append(opts.ModelAliases, proxy.ModelAlias{
			ID:          alias.ID,
			DisplayName: alias.DisplayName,
			Backend:     alias.Backend,
			Model:       alias.Model,
		})
	}
	for _, name := range []string{"deepseek", "openai", "custom"} {
		b, ok := l.EffectiveBackend(name)
		if !ok {
			continue
		}
		opts.Backends = append(opts.Backends, proxy.BackendOptions{
			Name:         name,
			APIKey:       b.APIKey,
			BaseURL:      b.BaseURL,
			UpstreamMode: b.UpstreamMode,
			ModelPattern: b.ModelPattern,
		})
	}
	return opts
}

// ProxyAddress is the loopback address claude-science should talk to.
func ProxyAddress(l config.Local) string {
	return net.JoinHostPort(loopbackHost(l.ProxyHost), strconv.Itoa(l.ProxyPort))
}

func loopbackHost(host string) string {
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	return host
}

func waitForTCP(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxy did not start on %s within %s: %w", addr, timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func reservePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserving a port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}
