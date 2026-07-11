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

type Config struct {
	SSHHost      string        `json:"ssh_host"`
	SSHPort      int           `json:"ssh_port"`
	SSHUser      string        `json:"ssh_user"`
	SSHKey       string        `json:"ssh_key"`
	ContainerID  string        `json:"container_id"`
	PortForwards []PortForward `json:"port_forwards"`
}

func DefaultConfig() Config {
	return Config{
		SSHPort:     22,
		SSHUser:     "root",
		ContainerID: "claude-science",
		PortForwards: []PortForward{
			{Local: 9876, Remote: 9876},
			{Local: 9981, Remote: 9981},
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

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

	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if cfg.SSHHost == "" {
		return nil, fmt.Errorf("ssh_host is required")
	}
	if cfg.SSHKey == "" {
		return nil, fmt.Errorf("ssh_key is required")
	}

	return &cfg, nil
}
