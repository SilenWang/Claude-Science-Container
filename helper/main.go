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

	targets, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	target := selectTarget(targets)
	if target == nil {
		log.Fatal("No target selected")
	}

	log.Printf("Fetching Claude Science URL from container '%s' ...", target.ContainerID)
	cmd := fmt.Sprintf("docker exec %s claude-science url", target.ContainerID)
	if output, err := runRemoteCommand(target, cmd); err != nil {
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

	forwardLoop(target, sigCh)

	log.Printf("Shutting down ...")
}

func selectTarget(targets []Target) *Target {
	if len(targets) == 1 {
		return &targets[0]
	}

	fmt.Println("\nAvailable targets:")
	for i, t := range targets {
		label := t.Name
		if label == "" {
			label = fmt.Sprintf("%s@%s:%d", t.SSHUser, t.SSHHost, t.SSHPort)
		}
		fmt.Printf("  [%d] %s — %s container %s", i+1, label, t.SSHHost, t.ContainerID)
		if len(t.PortForwards) > 0 {
			fmt.Printf(" (forwards: ")
			for j, pf := range t.PortForwards {
				if j > 0 {
					fmt.Printf(", ")
				}
				fmt.Printf(":%d→:%d", pf.Local, pf.Remote)
			}
			fmt.Printf(")")
		}
		fmt.Println()
	}

	fmt.Printf("\nSelect target [1-%d]: ", len(targets))
	var choice int
	for {
		if _, err := fmt.Scanf("%d", &choice); err != nil {
			fmt.Printf("Invalid input. Enter 1-%d: ", len(targets))
			continue
		}
		if choice < 1 || choice > len(targets) {
			fmt.Printf("Invalid choice. Enter 1-%d: ", len(targets))
			continue
		}
		break
	}

	return &targets[choice-1]
}

func forwardLoop(target *Target, sigCh chan os.Signal) {
	for {
		select {
		case <-sigCh:
			return
		default:
		}

		log.Printf("Connecting to %s@%s:%d ...", target.SSHUser, target.SSHHost, target.SSHPort)
		client, err := dialSSH(target)
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
		cleanup, err := startForwarding(client, target.PortForwards)
		if err != nil {
			log.Printf("Port forwarding setup failed: %v", err)
			client.Close()
			time.Sleep(3 * time.Second)
			continue
		}

		for _, pf := range target.PortForwards {
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
