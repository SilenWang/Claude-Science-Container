package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// runCLI keeps the original terminal workflow: pick a target, then keep its
// port forwarding alive until Ctrl-C.
func runCLI(configPath string) {
	targets, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	target := selectTarget(targets)
	if target == nil {
		log.Fatal("No target selected")
	}

	log.Printf("Target: %s@%s:%d", target.SSHUser, target.SSHHost, target.SSHPort)
	s := newSession(*target)
	s.Start(func(status, detail string, failed bool) {
		if detail == "" {
			log.Printf("%s", status)
			return
		}
		log.Printf("%s: %s", status, detail)
	})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("Shutting down ...")
	s.Stop()
	s.Wait()
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
		containerInfo := ""
		if t.ContainerID != "" {
			containerInfo = fmt.Sprintf(" (container: %s)", t.ContainerID)
		}
		fmt.Printf("  [%d] %s — %s%s", i+1, label, t.SSHHost, containerInfo)
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
