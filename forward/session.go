// Package forward keeps one SSH target's port forwards alive, extracting the
// login URL from a remote command and reporting every state change through a
// callback so the CLI and the GUI can share the same session implementation.
package forward

import (
	"fmt"
	"io"
	"log"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/SilenWang/Claude-Science-Container/config"
)

// Target and PortForward are the config types, aliased for convenience.
type Target = config.Target

// PortForward is one local → remote TCP forward.
type PortForward = config.PortForward

// Phase is the coarse state of a session.
type Phase string

const (
	// PhaseIdle is a session that has not started.
	PhaseIdle Phase = "idle"
	// PhaseConnecting is dialing the target or fetching the login URL.
	PhaseConnecting Phase = "connecting"
	// PhaseConnected is a session whose forwards are listening.
	PhaseConnected Phase = "connected"
	// PhaseError is a session waiting to retry after a failure.
	PhaseError Phase = "error"
	// PhaseStopped is a session Stop was called on.
	PhaseStopped Phase = "stopped"
)

// Status is one state change delivered to a Reporter.
type Status struct {
	Phase   Phase  `json:"phase"`
	Message string `json:"message,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Reporter receives every state change, from the session goroutine.
// Implementations must not block: the forward loop waits on them.
type Reporter func(Status)

// Conn is the slice of *ssh.Client a session needs. Tests substitute a fake.
type Conn interface {
	Run(command string) (string, error)
	Dial(network, addr string) (net.Conn, error)
	Keepalive() error
	Close() error
}

// DialFunc dials a target. The default speaks SSH with the configured key.
type DialFunc func(Target) (Conn, error)

// ListenFunc creates a local listener. The default listens on 127.0.0.1.
type ListenFunc func(network, addr string) (net.Listener, error)

// Options tunes a session. Zero values get the helper's historical defaults.
type Options struct {
	Dial              DialFunc
	Listen            ListenFunc
	RetryDelay        time.Duration
	KeepaliveInterval time.Duration
	AutoOpenBrowser   bool
	OpenBrowser       func(url string) error
	Logger            *log.Logger
}

func (o Options) withDefaults() Options {
	if o.Dial == nil {
		o.Dial = DialSSH
	}
	if o.Listen == nil {
		o.Listen = net.Listen
	}
	if o.RetryDelay == 0 {
		o.RetryDelay = 3 * time.Second
	}
	if o.KeepaliveInterval == 0 {
		o.KeepaliveInterval = 10 * time.Second
	}
	if o.OpenBrowser == nil {
		o.OpenBrowser = OpenInBrowser
	}
	if o.Logger == nil {
		o.Logger = log.Default()
	}
	return o
}

// Session keeps one target's forwards alive and reconnects after failures.
type Session struct {
	target config.Target
	opts   Options

	stopOnce sync.Once
	started  bool
	startMu  sync.Mutex
	stopCh   chan struct{}
	doneCh   chan struct{}

	mu     sync.Mutex
	status Status
	url    string
	err    error
}

// NewSession builds a session; call Start to run it.
func NewSession(target config.Target, opts Options) *Session {
	return &Session{
		target: target,
		opts:   opts.withDefaults(),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
		status: Status{Phase: PhaseIdle},
	}
}

// Start runs the session in the background and reports every state change.
// Only the first call has an effect.
func (s *Session) Start(report Reporter) {
	s.startMu.Lock()
	if s.started {
		s.startMu.Unlock()
		return
	}
	s.started = true
	s.startMu.Unlock()
	go s.run(report)
}

// Stop asks the session to shut down; Wait blocks until it has.
func (s *Session) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

// Wait blocks until the session goroutine has exited.
func (s *Session) Wait() {
	<-s.doneCh
}

// URL returns the last login URL seen, if any.
func (s *Session) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

// Status returns the last state change.
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Err returns the last fatal error, if the session exited because of one.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Session) run(report Reporter) {
	defer close(s.doneCh)

	s.logf("Target: %s@%s:%d", s.target.SSHUser, s.target.SSHHost, s.target.SSHPort)
	s.report(report, Status{
		Phase:   PhaseConnecting,
		Message: fmt.Sprintf("connecting to %s@%s:%d", s.target.SSHUser, s.target.SSHHost, s.target.SSHPort),
	})

	fetchedURL := false
	for {
		if s.stopping() {
			s.report(report, Status{Phase: PhaseStopped, Message: "stopped"})
			return
		}

		conn, err := s.opts.Dial(s.target)
		if err != nil {
			s.report(report, Status{Phase: PhaseError, Message: fmt.Sprintf("SSH connection failed, retry in %s: %v", s.opts.RetryDelay, err)})
			if !s.sleep(s.opts.RetryDelay) {
				s.report(report, Status{Phase: PhaseStopped, Message: "stopped"})
				return
			}
			continue
		}
		s.logf("SSH connection established")

		if !fetchedURL {
			fetchedURL = true
			s.fetchLoginURL(conn, report)
		}

		cleanup, keepaliveDone, stopKeepalive := s.startForwarding(conn, report)
		if cleanup == nil {
			// startForwarding already reported the error and closed the conn.
			if !s.sleep(s.opts.RetryDelay) {
				s.report(report, Status{Phase: PhaseStopped, Message: "stopped"})
				return
			}
			continue
		}

		for _, pf := range s.target.PortForwards {
			s.logf("  Forward: 127.0.0.1:%d -> remote:%d", pf.Local, pf.Remote)
		}
		s.report(report, Status{Phase: PhaseConnected, Message: s.forwardSummary(), URL: s.URL()})

		select {
		case <-s.stopCh:
			cleanup()
			stopKeepalive()
			conn.Close()
			s.report(report, Status{Phase: PhaseStopped, Message: "stopped"})
			return
		case <-keepaliveDone:
			cleanup()
			stopKeepalive()
			conn.Close()
			s.report(report, Status{
				Phase:   PhaseError,
				Message: fmt.Sprintf("connection lost, reconnecting in %s", s.opts.RetryDelay),
				URL:     s.URL(),
			})
			if !s.sleep(s.opts.RetryDelay) {
				s.report(report, Status{Phase: PhaseStopped, Message: "stopped"})
				return
			}
		}
	}
}

// fetchLoginURL runs the target command once and surfaces the URL it prints.
func (s *Session) fetchLoginURL(conn Conn, report Reporter) {
	command := TargetCommand(s.target)
	if command == "" {
		return
	}
	s.logf("Running command on %s@%s: %s", s.target.SSHUser, s.target.SSHHost, command)
	output, err := conn.Run(command)
	if err != nil {
		s.logf("Warning: could not fetch URL: %v", err)
		s.report(report, Status{Phase: PhaseConnecting, Message: fmt.Sprintf("remote command failed: %v", err), URL: s.URL()})
		return
	}
	url := ParseLoginURL(output)
	if url == "" {
		s.logf("Could not extract URL from output: %s", strings.TrimSpace(output))
		s.report(report, Status{Phase: PhaseConnecting, Message: "no login URL in remote command output", URL: s.URL()})
		return
	}

	s.mu.Lock()
	s.url = url
	s.mu.Unlock()
	s.report(report, Status{Phase: PhaseConnecting, Message: "login URL: " + url, URL: url})

	if s.opts.AutoOpenBrowser {
		if err := s.opts.OpenBrowser(url); err != nil {
			s.logf("Warning: could not open browser: %v", err)
		}
	}
}

// startForwarding opens every local port and proxies accepted connections over
// the SSH conn. A nil cleanup means setup failed.
func (s *Session) startForwarding(conn Conn, report Reporter) (cleanup func(), keepaliveDone <-chan struct{}, stopKeepalive func()) {
	var listeners []net.Listener
	for _, pf := range s.target.PortForwards {
		listener, err := s.opts.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", pf.Local))
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			conn.Close()
			s.report(report, Status{Phase: PhaseError, Message: fmt.Sprintf("listen :%d failed: %v", pf.Local, err)})
			return nil, nil, nil
		}
		listeners = append(listeners, listener)
		remoteAddr := fmt.Sprintf("127.0.0.1:%d", pf.Remote)
		go acceptLoop(conn, listener, remoteAddr)
	}

	keepaliveDone, stopKeepalive = startKeepalive(conn, s.opts.KeepaliveInterval)
	cleanup = func() {
		for _, l := range listeners {
			l.Close()
		}
	}
	return cleanup, keepaliveDone, stopKeepalive
}

func (s *Session) forwardSummary() string {
	parts := make([]string, 0, len(s.target.PortForwards))
	for _, pf := range s.target.PortForwards {
		parts = append(parts, fmt.Sprintf("%d→%d", pf.Local, pf.Remote))
	}
	if len(parts) == 0 {
		return "connected"
	}
	return "forwarding " + strings.Join(parts, ", ")
}

func (s *Session) report(report Reporter, status Status) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
	if status.Message != "" {
		s.logf("%s", status.Message)
	}
	if report != nil {
		report(status)
	}
}

func (s *Session) stopping() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *Session) sleep(d time.Duration) bool {
	select {
	case <-s.stopCh:
		return false
	case <-time.After(d):
		return true
	}
}

func (s *Session) logf(format string, args ...any) {
	if s.opts.Logger != nil {
		s.opts.Logger.Printf(format, args...)
	}
}

// TargetCommand returns the command a session runs remotely, wrapping it in
// docker exec when the target points at a container.
func TargetCommand(t config.Target) string {
	if t.Command == "" {
		return ""
	}
	if t.ContainerID != "" {
		return fmt.Sprintf("docker exec %s %s", t.ContainerID, t.Command)
	}
	return t.Command
}

var loginURLRe = regexp.MustCompile(`https?://[^\s"']+`)

// ParseLoginURL extracts the first http(s) URL from command output.
func ParseLoginURL(output string) string {
	return strings.TrimSpace(loginURLRe.FindString(output))
}

func acceptLoop(conn Conn, listener net.Listener, remoteAddr string) {
	for {
		localConn, err := listener.Accept()
		if err != nil {
			return
		}
		go handleForward(conn, localConn, remoteAddr)
	}
}

func handleForward(conn Conn, localConn net.Conn, remoteAddr string) {
	defer localConn.Close()

	remoteConn, err := conn.Dial("tcp", remoteAddr)
	if err != nil {
		return
	}
	defer remoteConn.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(remoteConn, localConn); done <- struct{}{} }()
	go func() { io.Copy(localConn, remoteConn); done <- struct{}{} }()
	<-done
}

// startKeepalive pings the connection and closes lost when a ping fails.
func startKeepalive(conn Conn, interval time.Duration) (lost <-chan struct{}, stop func()) {
	lostCh := make(chan struct{})
	stopCh := make(chan struct{})
	var stopOnce, lostOnce sync.Once
	lost = lostCh

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.Keepalive(); err != nil {
					lostOnce.Do(func() { close(lostCh) })
					return
				}
			case <-stopCh:
				return
			}
		}
	}()
	stop = func() { stopOnce.Do(func() { close(stopCh) }) }
	return lost, stop
}

// OpenInBrowser opens url with the platform's default browser.
func OpenInBrowser(url string) error {
	return openBrowser(url)
}
