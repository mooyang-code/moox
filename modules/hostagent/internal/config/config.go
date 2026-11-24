package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
)

// Config 是 HostAgent 的业务配置，全部由部署渲染（moox-cli setup）填写。
type Config struct {
	Version int `yaml:"version"`
	// HostID 是 Admin 登记的主机 ID，必填：主机快照的 subject_id、Monitor 的主机记录都以它为键。
	HostID         string `yaml:"host_id"`
	HostName       string `yaml:"host_name"`
	EventBusConfig string `yaml:"eventbus_config"`
	// HostAgent 定期经本机网关从 Monitor 拉取主机策略，写入 HostPolicyPath（<部署根>/data/host-agent/host-policy.json）；
	// 网关目录缓存也放在同一目录。
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
		return nil, fmt.Errorf("读取 host-agent 配置: %w", err)
	}
	cfg := &Config{Version: 1}
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析 host-agent 配置: %w", err)
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	cfg.HostID = strings.TrimSpace(cfg.HostID)
	cfg.HostName = strings.TrimSpace(cfg.HostName)
	cfg.EventBusConfig = strings.TrimSpace(cfg.EventBusConfig)
	cfg.HostPolicyPath = strings.TrimSpace(cfg.HostPolicyPath)
	if !servicecatalog.ValidHostID(cfg.HostID) {
		return nil, fmt.Errorf("host_id 必填，且须是登记的主机 ID（小写字母开头，只含小写字母、数字和连字符）")
	}
	if !filepath.IsAbs(cfg.EventBusConfig) || !filepath.IsAbs(cfg.HostPolicyPath) {
		return nil, fmt.Errorf("eventbus_config 与 host_policy_path 必填，且须是绝对路径")
	}
	if cfg.GatewayClient.Caller != "host-agent" {
		return nil, fmt.Errorf("gateway_client.caller 须为 host-agent")
	}
	if cfg.sourcePath, err = filepath.Abs(path); err != nil {
		return nil, fmt.Errorf("解析 host-agent 配置路径: %w", err)
	}
	return cfg, nil
}

// OpenGateway 打开经本机网关访问 Monitor 的客户端；目录缓存放在主机策略所在的数据目录。
func (c *Config) OpenGateway(onRefreshError func(error)) (*gatewayclient.Client, error) {
	if c.sourcePath == "" {
		return nil, fmt.Errorf("host-agent 配置未经 Load 载入，无法打开网关客户端")
	}
	return c.GatewayClient.OpenInternal(c.sourcePath, filepath.Dir(c.HostPolicyPath), onRefreshError)
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
	if cfg.CAFile != "" && !filepath.IsAbs(cfg.CAFile) {
		cfg.CAFile = filepath.Join(filepath.Dir(path), cfg.CAFile)
	}
	return cfg, nil
}
