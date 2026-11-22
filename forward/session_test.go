package forward

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SilenWang/Claude-Science-Container/config"
)

type fakeConn struct {
	mu sync.Mutex

	output        string
	runErr        error
	runCalls      []string
	keepaliveErr  error
	keepaliveFail int // fail on the Nth keepalive (1-based); 0 never fails
	keepalives    int
	closed        bool
}

func (c *fakeConn) Run(command string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runCalls = append(c.runCalls, command)
	return c.output, c.runErr
}

func (c *fakeConn) Dial(network, addr string) (net.Conn, error) {
	return nil, errors.New("fake conn cannot dial")
}

func (c *fakeConn) Keepalive() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keepalives++
	if c.keepaliveFail > 0 && c.keepalives >= c.keepaliveFail {
		return c.keepaliveErr
	}
	return nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.runCalls...)
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

type dialer struct {
	mu      sync.Mutex
	calls   int
	failFor int
	conns   []*fakeConn
	build   func(n int) (*fakeConn, error)
}

func (d *dialer) dial(config.Target) (Conn, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.mu.Unlock()
	if n <= d.failFor {
		return nil, fmt.Errorf("dial attempt %d failed", n)
	}
	if d.build != nil {
		conn, err := d.build(n)
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	conn := &fakeConn{}
	d.mu.Lock()
	d.conns = append(d.conns, conn)
	d.mu.Unlock()
	return conn, nil
}

func (d *dialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

type recorder struct {
	mu       sync.Mutex
	statuses []Status
}

func (r *recorder) report(status Status) {
	r.mu.Lock()
	r.statuses = append(r.statuses, status)
	r.mu.Unlock()
}

func (r *recorder) all() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Status(nil), r.statuses...)
}

func (r *recorder) waitFor(t *testing.T, timeout time.Duration, what string, pred func(Status) bool) Status {
	t.Helper()
	idx := r.waitForIndex(t, 0, timeout, what, pred)
	return r.all()[idx]
}

// waitForIndex waits for a matching status at or after from, returning its index.
func (r *recorder) waitForIndex(t *testing.T, from int, timeout time.Duration, what string, pred func(Status) bool) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		statuses := r.all()
		for i := from; i < len(statuses); i++ {
			if pred(statuses[i]) {
				return i
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; statuses: %#v", what, r.all())
	return -1
}

func testOptions(d DialFunc) Options {
	return Options{
		Dial:              d,
		RetryDelay:        5 * time.Millisecond,
		KeepaliveInterval: time.Hour,
		Logger:            log.New(io.Discard, "", 0),
	}
}

func TestSessionFetchesURLAndForwards(t *testing.T) {
	const url = "http://127.0.0.1:9981/?token=abc"
	conn := &fakeConn{output: "booting...\nLogin URL: " + url + "\n"}
	d := &dialer{build: func(int) (*fakeConn, error) { return conn, nil }}

	var browsers []string
	opts := testOptions(d.dial)
	opts.AutoOpenBrowser = true
	opts.OpenBrowser = func(u string) error {
		browsers = append(browsers, u)
		return nil
	}

	target := config.Target{
		Name:         "box",
		SSHHost:      "192.0.2.1",
		SSHPort:      22,
		SSHUser:      "root",
		SSHKey:       "/k",
		Command:      "claude-science url",
		PortForwards: []config.PortForward{{Local: 0, Remote: 9981}},
	}
	session := NewSession(target, opts)
	rec := &recorder{}
	session.Start(rec.report)

	rec.waitFor(t, 2*time.Second, "connected", func(s Status) bool {
		return s.Phase == PhaseConnected
	})
	if got := session.URL(); got != url {
		t.Fatalf("URL() = %q, want %q", got, url)
	}
	if calls := conn.calls(); len(calls) != 1 || calls[0] != "claude-science url" {
		t.Fatalf("Run calls = %#v", calls)
	}
	if len(browsers) != 1 || browsers[0] != url {
		t.Fatalf("browsers = %#v", browsers)
	}

	session.Stop()
	session.Wait()

	if !conn.isClosed() {
		t.Fatal("connection was not closed after Stop")
	}
	last := rec.all()[len(rec.all())-1]
	if last.Phase != PhaseStopped {
		t.Fatalf("last status = %#v, want stopped", last)
	}
}

func TestSessionWrapsContainerCommand(t *testing.T) {
	conn := &fakeConn{output: "http://example.test/login"}
	d := &dialer{build: func(int) (*fakeConn, error) { return conn, nil }}
	target := config.Target{
		SSHHost:      "192.0.2.2",
		SSHUser:      "root",
		SSHKey:       "/k",
		ContainerID:  "claude-science",
		Command:      "claude-science url",
		PortForwards: []config.PortForward{{Local: 0, Remote: 9981}},
	}
	session := NewSession(target, testOptions(d.dial))
	rec := &recorder{}
	session.Start(rec.report)
	rec.waitFor(t, 2*time.Second, "connected", func(s Status) bool { return s.Phase == PhaseConnected })
	session.Stop()
	session.Wait()

	want := "docker exec claude-science claude-science url"
	if calls := conn.calls(); len(calls) != 1 || calls[0] != want {
		t.Fatalf("Run calls = %#v, want %q", calls, want)
	}
}

func TestSessionRetriesFailedDial(t *testing.T) {
	conn := &fakeConn{output: "http://example.test/login"}
	d := &dialer{failFor: 2, build: func(int) (*fakeConn, error) { return conn, nil }}
	target := config.Target{
		SSHHost:      "192.0.2.3",
		SSHUser:      "root",
		SSHKey:       "/k",
		Command:      "claude-science url",
		PortForwards: []config.PortForward{{Local: 0, Remote: 9981}},
	}
	session := NewSession(target, testOptions(d.dial))
	rec := &recorder{}
	session.Start(rec.report)
	rec.waitFor(t, 3*time.Second, "connected after retries", func(s Status) bool { return s.Phase == PhaseConnected })
	if got := d.callCount(); got < 3 {
		t.Fatalf("dial calls = %d, want >= 3", got)
	}
	errorSeen := false
	for _, status := range rec.all() {
		if status.Phase == PhaseError && strings.Contains(status.Message, "SSH connection failed") {
			errorSeen = true
		}
	}
	if !errorSeen {
		t.Fatalf("no retry status recorded: %#v", rec.all())
	}
	session.Stop()
	session.Wait()
}

func TestSessionReconnectsOnKeepaliveLoss(t *testing.T) {
	first := &fakeConn{output: "http://example.test/first"}
	first.keepaliveFail = 1
	first.keepaliveErr = errors.New("keepalive timeout")
	second := &fakeConn{output: "http://example.test/second"}

	d := &dialer{build: func(n int) (*fakeConn, error) {
		if n == 1 {
			return first, nil
		}
		return second, nil
	}}
	target := config.Target{
		SSHHost:      "192.0.2.4",
		SSHUser:      "root",
		SSHKey:       "/k",
		Command:      "claude-science url",
		PortForwards: []config.PortForward{{Local: 0, Remote: 9981}},
	}
	opts := testOptions(d.dial)
	opts.KeepaliveInterval = 5 * time.Millisecond
	session := NewSession(target, opts)
	rec := &recorder{}
	session.Start(rec.report)

	errIdx := rec.waitForIndex(t, 0, 3*time.Second, "reconnect attempt", func(s Status) bool {
		return s.Phase == PhaseError && strings.Contains(s.Message, "connection lost")
	})
	rec.waitForIndex(t, errIdx+1, 3*time.Second, "second connection", func(s Status) bool {
		return s.Phase == PhaseConnected
	})
	if got := d.callCount(); got < 2 {
		t.Fatalf("dial calls = %d, want >= 2", got)
	}
	if !first.isClosed() {
		t.Fatal("lost connection was not closed")
	}
	session.Stop()
	session.Wait()
}

func TestSessionStopWhileConnecting(t *testing.T) {
	block := make(chan struct{})
	d := DialFunc(func(config.Target) (Conn, error) {
		<-block
		return nil, errors.New("stopped")
	})
	target := config.Target{SSHHost: "192.0.2.5", SSHUser: "root", SSHKey: "/k"}
	session := NewSession(target, testOptions(d))
	session.Start(nil)
	time.Sleep(10 * time.Millisecond)
	session.Stop()
	close(block)
	session.Wait()
}

func TestStartOnlyRunsOnce(t *testing.T) {
	conn := &fakeConn{output: "http://example.test/x"}
	d := &dialer{build: func(int) (*fakeConn, error) { return conn, nil }}
	target := config.Target{SSHHost: "h", SSHUser: "u", SSHKey: "/k", Command: "cmd", PortForwards: nil}
	session := NewSession(target, testOptions(d.dial))
	rec := &recorder{}
	session.Start(rec.report)
	session.Start(rec.report)
	time.Sleep(20 * time.Millisecond)
	session.Stop()
	session.Wait()
	if got := d.callCount(); got != 1 {
		t.Fatalf("dial calls = %d, want 1", got)
	}
}

func TestParseLoginURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Login at http://127.0.0.1:9981/?token=abc\n", "http://127.0.0.1:9981/?token=abc"},
		{`open "https://example.test/login?x=1"`, "https://example.test/login?x=1"},
		{"no url here", ""},
		{"  http://a.test/x  ", "http://a.test/x"},
	}
	for _, tc := range cases {
		if got := ParseLoginURL(tc.in); got != tc.want {
			t.Errorf("ParseLoginURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTargetCommand(t *testing.T) {
	if got := TargetCommand(config.Target{Command: "claude-science url"}); got != "claude-science url" {
		t.Fatalf("got %q", got)
	}
	if got := TargetCommand(config.Target{ContainerID: "cs", Command: "url"}); got != "docker exec cs url" {
		t.Fatalf("got %q", got)
	}
	if got := TargetCommand(config.Target{}); got != "" {
		t.Fatalf("got %q", got)
	}
}
