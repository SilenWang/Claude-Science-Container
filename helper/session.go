package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// sessionPhase is what a session is doing, as the window shows it.
type sessionPhase int

const (
	// phaseIdle is a target nothing has been started for.
	phaseIdle sessionPhase = iota
	// phaseConnecting is fetching the login URL or dialing.
	phaseConnecting
	// phaseConnected is forwarding with the tunnel up.
	phaseConnected
	// phaseError is retrying after a failure; detail says why.
	phaseError
)

// reportFn receives every change of a session's state, from the session's
// goroutine.
type reportFn func(phase sessionPhase, status, detail string)

// session keeps one target's SSH port forwarding alive, reconnecting when
// the connection drops, and remembers the login URL it last saw.
type session struct {
	target Target

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}

	mu  sync.Mutex
	url string
}

func newSession(target Target) *session {
	return &session{
		target: target,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start runs the session in the background and reports every change of
// state through report, which is called from the session goroutine.
func (s *session) Start(report reportFn) {
	go s.run(report)
}

func (s *session) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

func (s *session) setURL(url string) {
	s.mu.Lock()
	s.url = url
	s.mu.Unlock()
}

func (s *session) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *session) Wait() {
	<-s.doneCh
}

func (s *session) stopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

// sleep waits for d, and reports whether the session is still running.
func (s *session) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-s.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func (s *session) run(report reportFn) {
	defer close(s.doneCh)

	target := &s.target
	command := target.Command
	if target.ContainerID != "" {
		command = fmt.Sprintf("docker exec %s %s", target.ContainerID, target.Command)
	}

	// The login URL is a convenience: when the command fails the tunnel is
	// still worth opening, so the failure is reported but the flow goes on.
	report(phaseConnecting, "正在获取登录地址", "")
	output, err := runRemoteCommand(target, command)
	loginURL := parseURL(output)
	switch {
	case err != nil:
		report(phaseConnecting, "正在建立隧道", err.Error())
	case loginURL == "":
		report(phaseConnecting, "正在建立隧道", "命令输出里没有登录地址："+strings.TrimSpace(output))
	default:
		s.setURL(loginURL)
		report(phaseConnecting, "正在打开浏览器", "")
		openBrowser(loginURL)
	}

	for {
		if s.stopped() {
			return
		}

		client, err := dialSSH(target)
		if err != nil {
			report(phaseError, "无法连接，3 秒后重试", err.Error())
			if !s.sleep(3 * time.Second) {
				return
			}
			continue
		}

		kaDone := startKeepalive(client)
		cleanup, err := startForwarding(client, target.PortForwards)
		if err != nil {
			client.Close()
			report(phaseError, "端口转发失败，3 秒后重试", err.Error())
			if !s.sleep(3 * time.Second) {
				return
			}
			continue
		}

		if s.URL() == "" {
			report(phaseConnected, "已连接", "未获取到登录地址，请手动打开转发端口")
		} else {
			report(phaseConnected, "已连接", "")
		}
		select {
		case <-s.stopCh:
			cleanup()
			client.Close()
			return
		case <-kaDone:
			cleanup()
			client.Close()
			report(phaseError, "连接已断开，3 秒后重连", "")
			if !s.sleep(3 * time.Second) {
				return
			}
		}
	}
}

func parseURL(output string) string {
	re := regexp.MustCompile(`https?://[^\s"']+`)
	return strings.TrimSpace(re.FindString(output))
}

func openBrowser(url string) {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		cmd = "xdg-open"
		args = []string{url}
	}

	proc := exec.Command(cmd, args...)
	if err := proc.Start(); err != nil {
		return
	}
	proc.Process.Release()
}
