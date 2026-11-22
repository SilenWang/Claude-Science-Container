package forward

import (
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/SilenWang/Claude-Science-Container/config"
)

// DialSSH is the production dialer: key-based SSH with keepalive-friendly TCP.
func DialSSH(target config.Target) (Conn, error) {
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

	return &sshConn{client: ssh.NewClient(conn, chans, reqs)}, nil
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

// sshConn adapts *ssh.Client to the small Conn interface sessions use.
type sshConn struct {
	client *ssh.Client
}

func (c *sshConn) Run(command string) (string, error) {
	session, err := c.client.NewSession()
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

func (c *sshConn) Dial(network, addr string) (net.Conn, error) {
	return c.client.Dial(network, addr)
}

func (c *sshConn) Keepalive() error {
	_, _, err := c.client.SendRequest("keepalive@openssh.com", true, nil)
	return err
}

func (c *sshConn) Close() error {
	return c.client.Close()
}
