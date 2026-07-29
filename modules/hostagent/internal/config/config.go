package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version        int    `yaml:"version"`
	IdentityPath   string `yaml:"identity_path"`
	EventBusConfig string `yaml:"eventbus_config"`
	HealthAddr     string `yaml:"health_addr"`
	HostName       string `yaml:"host_name"`
}
type EventBusConfig struct {
	Version       int      `yaml:"version"`
	URLs          []string `yaml:"urls"`
	Username      string   `yaml:"username"`
	EventBusToken string   `yaml:"eventbus_token"`
	CAFile        string   `yaml:"ca_file"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read hostagent config: %w", err)
	}
	cfg := &Config{Version: 1, IdentityPath: "~/.local/state/moox/hostagent/identity.yaml", EventBusConfig: "~/.config/moox/hostagent/eventbus.yaml", HealthAddr: "127.0.0.1:11425"}
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse hostagent config: %w", err)
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.IdentityPath == "" || cfg.EventBusConfig == "" {
		return nil, fmt.Errorf("identity_path and eventbus_config are required")
	}
	if healthAddr := strings.TrimSpace(os.Getenv("MOOX_HOST_AGENT_HEALTH_ADDR")); healthAddr != "" {
		cfg.HealthAddr = healthAddr
	}
	cfg.IdentityPath, cfg.EventBusConfig = Expand(cfg.IdentityPath), Expand(cfg.EventBusConfig)
	cfg.HostName = strings.TrimSpace(cfg.HostName)
	return cfg, nil
}

func LoadEventBus(path string) (EventBusConfig, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return EventBusConfig{}, fmt.Errorf("stat eventbus config: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return EventBusConfig{}, fmt.Errorf("eventbus config must be a regular 0600 file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return EventBusConfig{}, fmt.Errorf("read eventbus config: %w", err)
	}
	var cfg EventBusConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse eventbus config: %w", err)
	}
	if len(cfg.URLs) == 0 || strings.TrimSpace(cfg.Username) == "" || strings.TrimSpace(cfg.EventBusToken) == "" {
		return cfg, fmt.Errorf("eventbus config requires urls, username, and eventbus_token")
	}
	cfg.CAFile = Expand(cfg.CAFile)
	if cfg.CAFile != "" && !filepath.IsAbs(cfg.CAFile) {
		cfg.CAFile = filepath.Join(filepath.Dir(path), cfg.CAFile)
	}
	return cfg, nil
}
func Expand(path string) string {
	if strings.HasPrefix(path, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return os.ExpandEnv(path)
}
