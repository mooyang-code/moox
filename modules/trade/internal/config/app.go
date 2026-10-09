// Package config 提供 Trade 模块的应用配置加载。
//
// Trade 模块使用独立的 SQLite 库（账户域 + 交易域同库）。
// trpc_go.yaml 由 trpc-go 运行时自动加载，本包只加载业务侧 app.yaml。
package config

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// AppConfig Trade 应用配置。
type AppConfig struct {
	Database      DatabaseConfig           `yaml:"database"`
	GatewayClient gatewayclient.FileConfig `yaml:"gateway_client"`
	sourcePath    string                   `yaml:"-"`
	EventBus      EventBusConfig           `yaml:"eventbus"`
	Runtime       RuntimeConfig            `yaml:"runtime"`
	DNSResolver   DNSResolverConfig        `yaml:"dns_resolver"`
}

// DNSResolverConfig is rendered from the sanitized dns_resolver section in
// moox.toml by moox-cli. Trade never reads moox.toml directly.
type DNSResolverConfig struct {
	Enabled         bool     `yaml:"enabled"`
	Domains         []string `yaml:"domains"`
	LookupTimeoutMS int      `yaml:"lookup_timeout_ms"`
	ProbeTimeoutMS  int      `yaml:"probe_timeout_ms"`
	ProbePort       int      `yaml:"probe_port"`
	CacheTTLSeconds int      `yaml:"cache_ttl_seconds"`
	MaxIPsPerDomain int      `yaml:"max_ips_per_domain"`
}

// DatabaseConfig 数据库配置（当前仅支持 sqlite）。
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// RuntimeConfig contains process-local execution settings.
type RuntimeConfig struct {
	LiveTradingEnabled bool `yaml:"live_trading_enabled"`
}

type EventBusConfig struct {
	Enabled        bool     `yaml:"enabled"`
	URLs           []string `yaml:"urls"`
	CredentialFile string   `yaml:"credential_file"`
	TargetConsumer string   `yaml:"-"`
}

const TargetConsumer = "trade_target_weight_v1"

// DefaultConfig 返回默认配置。
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Database: DatabaseConfig{
			Path: "./data/moox_trade.db",
		},
		Runtime:       RuntimeConfig{},
		GatewayClient: gatewayclient.FileConfig{Caller: "trade", KeyFile: "../../secrets/caller-trade.key"},
		EventBus:      EventBusConfig{Enabled: true, URLs: []string{"nats://127.0.0.1:4222"}, TargetConsumer: TargetConsumer},
		DNSResolver: DNSResolverConfig{
			LookupTimeoutMS: 1500,
			ProbeTimeoutMS:  500,
			ProbePort:       443,
			CacheTTLSeconds: 300,
			MaxIPsPerDomain: 4,
		},
	}
}

// Load 从文件加载配置，叠加默认值与环境变量覆盖。
func Load(configPath string) (*AppConfig, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	cfg.sourcePath = configPath
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trade config must contain exactly one YAML document")
	}
	if err := cfg.applyEnv(); err != nil {
		return nil, fmt.Errorf("invalid environment: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func (c *AppConfig) applyEnv() error {
	if v := os.Getenv("MOOX_TRADE_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_TRADE_LIVE_TRADING_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf(
				"MOOX_TRADE_LIVE_TRADING_ENABLED must be true or false: %w",
				err,
			)
		}
		c.Runtime.LiveTradingEnabled = enabled
	}
	return nil
}

// Validate 校验配置并创建所需目录。
func (c *AppConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("trade config is required")
	}
	if c.GatewayClient.Caller != "trade" {
		return fmt.Errorf("gateway_client.caller must be trade")
	}
	if err := c.GatewayClient.Validate(); err != nil {
		return err
	}
	if c.Database.Path == "" {
		return fmt.Errorf("database path is required")
	}
	dir := filepath.Dir(c.Database.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}
	if c.EventBus.Enabled {
		if len(c.EventBus.URLs) == 0 {
			return fmt.Errorf("eventbus urls are required")
		}
		if c.EventBus.TargetConsumer == "" {
			return fmt.Errorf("eventbus target consumer is required")
		}
	}
	if err := c.DNSResolver.Validate(); err != nil {
		return err
	}
	return nil
}

func (c DNSResolverConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if len(c.Domains) == 0 {
		return fmt.Errorf("dns_resolver domains are required when enabled")
	}
	if len(c.Domains) > 16 {
		return fmt.Errorf("dns_resolver supports at most 16 domains")
	}
	seen := make(map[string]struct{}, len(c.Domains))
	for _, raw := range c.Domains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if !validDNSResolverDomain(domain) {
			return fmt.Errorf("dns_resolver domain %q is invalid", raw)
		}
		if _, exists := seen[domain]; exists {
			return fmt.Errorf("dns_resolver domain %q is duplicated", raw)
		}
		seen[domain] = struct{}{}
	}
	if c.LookupTimeoutMS <= 0 {
		return fmt.Errorf("dns_resolver lookup_timeout_ms must be positive")
	}
	if c.ProbeTimeoutMS <= 0 {
		return fmt.Errorf("dns_resolver probe_timeout_ms must be positive")
	}
	if c.ProbePort < 1 || c.ProbePort > 65535 {
		return fmt.Errorf("dns_resolver probe_port must be between 1 and 65535")
	}
	if c.CacheTTLSeconds <= 0 {
		return fmt.Errorf("dns_resolver cache_ttl_seconds must be positive")
	}
	if c.MaxIPsPerDomain < 1 || c.MaxIPsPerDomain > 4 {
		return fmt.Errorf("dns_resolver max_ips_per_domain must be between 1 and 4")
	}
	return nil
}

func validDNSResolverDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || net.ParseIP(domain) != nil || strings.Contains(domain, "..") {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// OpenGateway creates the process client used by Admin secret reads.
func (c *AppConfig) OpenGateway(onRefreshError func(error)) (*gatewayclient.Client, error) {
	if c == nil || c.sourcePath == "" {
		return nil, fmt.Errorf("trade gateway client requires a loaded module configuration")
	}
	if c.GatewayClient.Caller != "trade" {
		return nil, fmt.Errorf("gateway_client.caller must be trade")
	}
	return c.GatewayClient.OpenInternal(c.sourcePath, filepath.Dir(c.Database.Path), onRefreshError)
}
