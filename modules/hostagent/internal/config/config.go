package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

type Config struct {
	HostID         string `yaml:"host_id"`
	Version        int    `yaml:"version"`
	IdentityPath   string `yaml:"identity_path"`
	EventBusConfig string `yaml:"eventbus_config"`
	HealthAddr     string `yaml:"health_addr"`
	HostName       string `yaml:"host_name"`
	// GatewayClient 与 HostPolicyPath 由部署渲染；两者都设置时 HostAgent 定期从 Monitor 拉取主机策略，
	// 写入 HostPolicyPath（<部署根>/run/host-policy.json）。独立运行的 HostAgent 留空，不拉取。
	GatewayClient  gatewayclient.FileConfig `yaml:"gateway_client"`
	HostPolicyPath string                   `yaml:"host_policy_path"`
	sourcePath     string
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
	cfg.HostID = strings.TrimSpace(cfg.HostID)
	cfg.HostPolicyPath = strings.TrimSpace(cfg.HostPolicyPath)
	if cfg.HostPolicyPath != "" {
		if !filepath.IsAbs(cfg.HostPolicyPath) {
			return nil, fmt.Errorf("host_policy_path must be absolute")
		}
		if cfg.GatewayClient.Caller != "host-agent" {
			return nil, fmt.Errorf("host_policy_path requires gateway_client.caller host-agent")
		}
	}
	if cfg.sourcePath, err = filepath.Abs(path); err != nil {
		return nil, fmt.Errorf("resolve hostagent config: %w", err)
	}
	return cfg, nil
}

// PolicySyncEnabled 表示是否从 Monitor 拉取主机策略。
func (c *Config) PolicySyncEnabled() bool { return c.HostPolicyPath != "" }

// OpenGateway 打开经本机网关访问 Monitor 的客户端；目录缓存放在身份文件所在的数据目录。
func (c *Config) OpenGateway(onRefreshError func(error)) (*gatewayclient.Client, error) {
	if !c.PolicySyncEnabled() || c.sourcePath == "" {
		return nil, fmt.Errorf("hostagent gateway client is not configured")
	}
	return c.GatewayClient.OpenInternal(c.sourcePath, filepath.Dir(c.IdentityPath), onRefreshError)
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
