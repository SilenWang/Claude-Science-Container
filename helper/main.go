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

	log.Printf("Connecting to %s@%s:%d ...", cfg.SSHUser, cfg.SSHHost, cfg.SSHPort)
	client, err := dialSSH(cfg)
	if err != nil {
		log.Fatalf("Failed to establish SSH connection: %v", err)
	}
	defer client.Close()

	log.Printf("SSH connection established to %s", cfg.SSHHost)

	go startKeepalive(client)

	log.Printf("Setting up port forwarding ...")
	for _, pf := range cfg.PortForwards {
		if err := forwardPort(client, pf.Local, pf.Remote); err != nil {
			log.Fatalf("Failed to set up port forward :%d -> :%d: %v", pf.Local, pf.Remote, err)
		}
		log.Printf("  Port forward active: 127.0.0.1:%d -> remote:%d", pf.Local, pf.Remote)
	}

	log.Printf("Fetching Claude Science URL from container '%s' ...", cfg.ContainerID)
	cmd := fmt.Sprintf("docker exec %s claude-science url", cfg.ContainerID)
	output, err := runRemoteCommand(client, cmd)
	if err != nil {
		log.Printf("Warning: could not fetch URL: %v", err)
	} else {
		url := parseURL(output)
		if url != "" {
			log.Printf("=== Claude Science Login URL ===")
			log.Printf("  %s", url)
			log.Printf("================================")
			openBrowser(url)
		} else {
			log.Printf("Could not extract URL from output: %s", strings.TrimSpace(output))
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Port forwarding active. Press Ctrl+C to stop.")
	<-sigCh

	log.Printf("Shutting down ...")
}

func parseURL(output string) string {
	re := regexp.MustCompile(`https?://[^\s"']+`)
	matches := re.FindString(output)
	return strings.TrimSpace(matches)
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

	if err := proc.Process.Release(); err != nil {
		log.Printf("Warning: could not release browser process: %v", err)
	}
}
