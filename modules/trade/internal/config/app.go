// Package config 提供 Trade 模块的应用配置加载。
//
// Trade 模块使用独立的 SQLite 库（账户域 + 交易域同库）。
// trpc_go.yaml 由 trpc-go 运行时自动加载，本包只加载业务侧 app.yaml。
package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// AppConfig Trade 应用配置。
type AppConfig struct {
	Database    DatabaseConfig    `yaml:"database"`
	Admin       AdminConfig       `yaml:"admin"`
	EventBus    EventBusConfig    `yaml:"eventbus"`
	Runtime     RuntimeConfig     `yaml:"runtime"`
	DNSResolver DNSResolverConfig `yaml:"dns_resolver"`
}

// DNSResolverConfig is rendered from the sanitized dns_resolver section in
// custom.toml by moox-cli. Trade never reads custom.toml directly.
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
	LiveTradingEnabled  bool   `yaml:"live_trading_enabled"`
	PaperInitialBalance string `yaml:"paper_initial_balance"`
}

// AdminConfig configures Trade access to Admin secrets.
type AdminConfig struct {
	BaseURL     string            `yaml:"base_url"`
	ServiceAuth ServiceAuthConfig `yaml:"service_auth"`
}

// ServiceAuthConfig 与 admin gateway.service_auth 保持一致。
type ServiceAuthConfig struct {
	AccessKey     string `yaml:"access_key"`
	SecretKey     string `yaml:"secret_key"`
	TargetNode    string `yaml:"target_node"`
	CAFile        string `yaml:"ca_file"`
	ExpireSeconds int64  `yaml:"expire_seconds"`
}

type EventBusConfig struct {
	Enabled        bool     `yaml:"enabled"`
	URLs           []string `yaml:"urls"`
	CredentialFile string   `yaml:"credential_file"`
	TargetConsumer string   `yaml:"-"`
}

const TargetConsumer = "trade_target_v1"

// DefaultConfig 返回默认配置。
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Database: DatabaseConfig{
			Path: "./data/moox_trade.db",
		},
		Runtime: RuntimeConfig{PaperInitialBalance: "100000"},
		Admin: AdminConfig{
			BaseURL: "https://106.53.107.122:11001",
			ServiceAuth: ServiceAuthConfig{
				AccessKey:     "moox-service",
				SecretKey:     "",
				ExpireSeconds: 60,
			},
		},
		EventBus: EventBusConfig{Enabled: true, URLs: []string{"nats://127.0.0.1:4222"}, TargetConsumer: TargetConsumer},
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
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("failed to read config file: %w", err)
			}
		} else {
			decoder := yaml.NewDecoder(bytes.NewReader(data))
			decoder.KnownFields(true)
			if err := decoder.Decode(cfg); err != nil {
				return nil, fmt.Errorf("failed to parse config file: %w", err)
			}
		}
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
	if v := os.Getenv("MOOX_TRADE_ADMIN_URL"); v != "" {
		c.Admin.BaseURL = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"); v != "" {
		c.Admin.ServiceAuth.AccessKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"); v != "" {
		c.Admin.ServiceAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_NODE_ID"); v != "" {
		c.Admin.ServiceAuth.TargetNode = v
	}
	if v := os.Getenv("MOOX_GATEWAY_CA_FILE"); v != "" {
		c.Admin.ServiceAuth.CAFile = v
	}
	return nil
}

// Validate 校验配置并创建所需目录。
func (c *AppConfig) Validate() error {
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
	paperBalance, err := shared.ParseDecimal(c.Runtime.PaperInitialBalance)
	if err != nil || paperBalance.Cmp(shared.Zero()) <= 0 {
		return fmt.Errorf("runtime paper_initial_balance must be a positive decimal")
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
