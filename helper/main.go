package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	configPath := "config.json"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Fetching Claude Science URL from container '%s' ...", cfg.ContainerID)
	cmd := fmt.Sprintf("docker exec %s claude-science url", cfg.ContainerID)
	if output, err := runRemoteCommand(cfg, cmd); err != nil {
		log.Printf("Warning: could not fetch URL: %v", err)
	} else if url := parseURL(output); url != "" {
		log.Printf("=== Claude Science Login URL ===")
		log.Printf("  %s", url)
		log.Printf("================================")
		openBrowser(url)
	} else {
		log.Printf("Could not extract URL from output: %s", strings.TrimSpace(output))
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	forwardLoop(cfg, sigCh)

	log.Printf("Shutting down ...")
}

func forwardLoop(cfg *Config, sigCh chan os.Signal) {
	for {
		select {
		case <-sigCh:
			return
		default:
		}

		log.Printf("Connecting to %s@%s:%d ...", cfg.SSHUser, cfg.SSHHost, cfg.SSHPort)
		client, err := dialSSH(cfg)
		if err != nil {
			log.Printf("SSH connection failed, retry in 3s: %v", err)
			select {
			case <-sigCh:
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		log.Printf("SSH connection established")

		kaDone := startKeepalive(client)
		cleanup, err := startForwarding(client, cfg.PortForwards)
		if err != nil {
			log.Printf("Port forwarding setup failed: %v", err)
			client.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		for _, pf := range cfg.PortForwards {
			log.Printf("  Forward: 127.0.0.1:%d -> remote:%d", pf.Local, pf.Remote)
		}

		select {
		case <-sigCh:
			cleanup()
			client.Close()
			return
		case <-kaDone:
			log.Printf("Connection lost, reconnecting in 3s ...")
			cleanup()
			client.Close()
			time.Sleep(3 * time.Second)
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
		log.Printf("Warning: could not open browser: %v", err)
		return
	}
	proc.Process.Release()
}
