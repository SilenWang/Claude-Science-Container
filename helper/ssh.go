package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

func dialSSH(target *Target) (*ssh.Client, error) {
	signer, err := loadSigner(target.SSHKey)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User:            target.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	addr := net.JoinHostPort(target.SSHHost, fmt.Sprintf("%d", target.SSHPort))

	tcpConn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("TCP dial %s: %w", addr, err)
	}

	if tcp, ok := tcpConn.(*net.TCPConn); ok {
		tcp.SetKeepAlive(true)
		tcp.SetKeepAlivePeriod(10 * time.Second)
		tcp.SetNoDelay(true)
	}

	conn, chans, reqs, err := ssh.NewClientConn(tcpConn, addr, sshCfg)
	if err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("SSH handshake %s: %w", addr, err)
	}

	return ssh.NewClient(conn, chans, reqs), nil
}

func loadSigner(keyPath string) (ssh.Signer, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading SSH key %s: %w", keyPath, err)
	}

	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("parsing SSH key: %w", err)
	}

	return signer, nil
}

func startKeepalive(client *ssh.Client) chan struct{} {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					log.Printf("SSH keepalive lost: %v", err)
					close(done)
					return
				}
			case <-done:
				return
			}
		}
	}()
	return done
}

func startForwarding(client *ssh.Client, forwards []PortForward) (func(), error) {
	var listeners []net.Listener
	for _, pf := range forwards {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", pf.Local))
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return nil, fmt.Errorf("listen :%d: %w", pf.Local, err)
		}
		listeners = append(listeners, listener)
		remoteAddr := fmt.Sprintf("127.0.0.1:%d", pf.Remote)
		go acceptLoop(client, listener, remoteAddr, pf.Local)
	}

	cleanup := func() {
		for _, l := range listeners {
			l.Close()
		}
	}
	return cleanup, nil
}

func acceptLoop(client *ssh.Client, listener net.Listener, remoteAddr string, localPort int) {
	for {
		localConn, err := listener.Accept()
		if err != nil {
			return
		}
		go handleForward(client, localConn, remoteAddr, localPort)
	}
}

func handleForward(client *ssh.Client, localConn net.Conn, remoteAddr string, localPort int) {
	defer localConn.Close()

	remoteConn, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		return
	}
	defer remoteConn.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(remoteConn, localConn); done <- struct{}{} }()
	go func() { io.Copy(localConn, remoteConn); done <- struct{}{} }()
	<-done
}

func runRemoteCommand(target *Target, command string) (string, error) {
	client, err := dialSSH(target)
	if err != nil {
		return "", fmt.Errorf("SSH dial for command: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("SSH session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return "", fmt.Errorf("remote command: %w (output: %s)", err, string(output))
	}

	return string(output), nil
}
