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
	HostID   string `yaml:"host_id"`
	HostName string `yaml:"host_name"`
	// HostAgent 每分钟经本机网关把主机快照报给 Monitor，同一次调用拉回主机策略，写入 HostPolicyPath
	// （<部署根>/data/host-agent/host-policy.json）；网关目录缓存也放在同一目录。
	GatewayClient  gatewayclient.FileConfig `yaml:"gateway_client"`
	HostPolicyPath string                   `yaml:"host_policy_path"`
	sourcePath     string
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
	cfg.HostPolicyPath = strings.TrimSpace(cfg.HostPolicyPath)
	if !servicecatalog.ValidHostID(cfg.HostID) {
		return nil, fmt.Errorf("host_id 必填，且须是登记的主机 ID（小写字母开头，只含小写字母、数字和连字符）")
	}
	if !filepath.IsAbs(cfg.HostPolicyPath) {
		return nil, fmt.Errorf("host_policy_path 必填，且须是绝对路径")
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
