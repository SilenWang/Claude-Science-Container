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
	Command      string        `json:"command"`
	PortForwards []PortForward `json:"port_forwards"`
}

type multiConfig struct {
	Targets []Target `json:"targets"`
}

func defaultTarget() Target {
	return Target{
		SSHPort: 22,
		SSHUser: "root",
		Command: "claude-science url",
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
	if t.Command == "" {
		t.Command = "claude-science url"
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
	if err := json.Unmarshal(data, &multi); err == nil && hasTargetsKey(data) {
		for i := range multi.Targets {
			applyDefaults(&multi.Targets[i])
			if err := validateTarget(&multi.Targets[i], i); err != nil {
				return nil, err
			}
		}
		// An empty list is a valid config: the window says there is nothing
		// to connect to yet, rather than reporting a parse error.
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

// hasTargetsKey reports whether the file is in the multi-target form, even
// when its list is empty: the legacy form has no "targets" field at all.
func hasTargetsKey(data []byte) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	_, ok := probe["targets"]
	return ok
}
