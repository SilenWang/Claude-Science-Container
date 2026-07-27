package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type PortForward struct {
	Local  int `json:"local"`
	Remote int `json:"remote"`
}

type Target struct {
	Name         string        `json:"name"`
	SSHHost      string        `json:"ssh_host"`
	SSHPort      int           `json:"ssh_port"`
	SSHUser      string        `json:"ssh_user"`
	SSHKey       string        `json:"ssh_key"`
	ContainerID  string        `json:"container_id"`
	PortForwards []PortForward `json:"port_forwards"`
}

type multiConfig struct {
	Targets []Target `json:"targets"`
}

func defaultTarget() Target {
	return Target{
		SSHPort:     22,
		SSHUser:     "root",
		ContainerID: "claude-science",
		PortForwards: []PortForward{
			{Local: 9876, Remote: 9876},
			{Local: 9981, Remote: 9981},
		},
	}
}

func applyDefaults(t *Target) {
	if t.SSHPort == 0 {
		t.SSHPort = 22
	}
	if t.SSHUser == "" {
		t.SSHUser = "root"
	}
	if t.ContainerID == "" {
		t.ContainerID = "claude-science"
	}
	if len(t.PortForwards) == 0 {
		t.PortForwards = []PortForward{
			{Local: 9876, Remote: 9876},
			{Local: 9981, Remote: 9981},
		}
	}
}

func validateTarget(t *Target, idx int) error {
	prefix := ""
	if idx >= 0 {
		prefix = fmt.Sprintf("target[%d]: ", idx)
	}
	if t.SSHHost == "" {
		return fmt.Errorf("%sssh_host is required", prefix)
	}
	if t.SSHKey == "" {
		return fmt.Errorf("%sssh_key is required", prefix)
	}
	return nil
}

func LoadConfig(path string) ([]Target, error) {
	if path == "" {
		return nil, fmt.Errorf("config path is required")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving config path: %w", err)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", absPath, err)
	}

	var multi multiConfig
	if err := json.Unmarshal(data, &multi); err == nil && len(multi.Targets) > 0 {
		for i := range multi.Targets {
			applyDefaults(&multi.Targets[i])
			if err := validateTarget(&multi.Targets[i], i); err != nil {
				return nil, err
			}
		}
		return multi.Targets, nil
	}

	var legacy Target
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	applyDefaults(&legacy)
	if err := validateTarget(&legacy, -1); err != nil {
		return nil, err
	}

	return []Target{legacy}, nil
}
