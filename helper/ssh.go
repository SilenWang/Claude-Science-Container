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

func dialSSH(cfg *Config) (*ssh.Client, error) {
	signer, err := loadSigner(cfg.SSHKey)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ClientConfig{
		User:            cfg.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	addr := net.JoinHostPort(cfg.SSHHost, fmt.Sprintf("%d", cfg.SSHPort))

	tcpConn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("TCP dial %s: %w", addr, err)
	}

	if tcp, ok := tcpConn.(*net.TCPConn); ok {
		tcp.SetKeepAlive(true)
		tcp.SetKeepAlivePeriod(15 * time.Second)
		tcp.SetNoDelay(true)
	}

	conn, chans, reqs, err := ssh.NewClientConn(tcpConn, addr, sshCfg)
	if err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("SSH handshake %s: %w", addr, err)
	}

	client := ssh.NewClient(conn, chans, reqs)
	return client, nil
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

func startKeepalive(client *ssh.Client) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		if err != nil {
			log.Printf("SSH keepalive error: %v", err)
			return
		}
	}
}

func forwardPort(client *ssh.Client, localPort, remotePort int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		return fmt.Errorf("listening on local :%d: %w", localPort, err)
	}

	remoteAddr := fmt.Sprintf("127.0.0.1:%d", remotePort)

	go func() {
		defer listener.Close()

		for {
			localConn, err := listener.Accept()
			if err != nil {
				log.Printf("Port forward accept error on :%d: %v", localPort, err)
				return
			}

			go handleForwardConn(client, localConn, remoteAddr, localPort)
		}
	}()

	return nil
}

func handleForwardConn(client *ssh.Client, localConn net.Conn, remoteAddr string, localPort int) {
	defer localConn.Close()

	remoteConn, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		log.Printf("SSH dial remote %s (forward :%d): %v", remoteAddr, localPort, err)
		return
	}
	defer remoteConn.Close()

	done := make(chan struct{}, 2)

	go func() {
		io.Copy(remoteConn, localConn)
		done <- struct{}{}
	}()

	go func() {
		io.Copy(localConn, remoteConn)
		done <- struct{}{}
	}()

	<-done
}

func runRemoteCommand(cfg *Config, command string) (string, error) {
	client, err := dialSSH(cfg)
	if err != nil {
		return "", fmt.Errorf("dialing SSH for command: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("creating SSH session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return "", fmt.Errorf("executing remote command: %w (output: %s)", err, string(output))
	}

	return string(output), nil
}
