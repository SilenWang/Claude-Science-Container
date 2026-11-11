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
func (s *session) Start(report func(status, detail string, failed bool)) {
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

func (s *session) run(report func(status, detail string, failed bool)) {
	defer close(s.doneCh)

	target := &s.target
	command := target.Command
	if target.ContainerID != "" {
		command = fmt.Sprintf("docker exec %s %s", target.ContainerID, target.Command)
	}

	report("正在获取登录地址…", "", false)
	output, err := runRemoteCommand(target, command)
	loginURL := parseURL(output)
	switch {
	case err != nil:
		report("无法获取登录地址，将只建立端口转发", err.Error(), true)
	case loginURL == "":
		report("未能从命令输出中找到登录地址", strings.TrimSpace(output), true)
	default:
		s.setURL(loginURL)
		report("已获取登录地址，正在打开浏览器…", loginURL, false)
		openBrowser(loginURL)
	}

	for {
		if s.stopped() {
			return
		}

		report(fmt.Sprintf("正在连接 %s@%s:%d …", target.SSHUser, target.SSHHost, target.SSHPort), s.URL(), false)
		client, err := dialSSH(target)
		if err != nil {
			report("SSH 连接失败，3 秒后重试", err.Error(), true)
			if !s.sleep(3 * time.Second) {
				return
			}
			continue
		}

		kaDone := startKeepalive(client)
		cleanup, err := startForwarding(client, target.PortForwards)
		if err != nil {
			client.Close()
			report("端口转发启动失败，3 秒后重试", err.Error(), true)
			if !s.sleep(3 * time.Second) {
				return
			}
			continue
		}

		status := "已连接，端口已转发"
		if s.URL() == "" {
			status += "（未获取到登录地址）"
		}
		report(status, s.URL(), false)
		select {
		case <-s.stopCh:
			cleanup()
			client.Close()
			return
		case <-kaDone:
			cleanup()
			client.Close()
			report("连接断开，3 秒后重连", "", true)
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
