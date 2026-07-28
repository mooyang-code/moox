// Package config 提供 Trade 模块的应用配置加载。
//
// Trade 模块使用独立的 SQLite 库（账户域 + 交易域同库），
// 并复用与 admin 一致的 AES 加密密钥用于 API 凭证加解密。
// trpc_go.yaml 由 trpc-go 运行时自动加载，本包只加载业务侧 app.yaml。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AppConfig Trade 应用配置。
type AppConfig struct {
	Database       DatabaseConfig       `yaml:"database"`
	Security       SecurityConfig       `yaml:"security"`
	ControlGateway ControlGatewayConfig `yaml:"control_gateway"`
	Sync           SyncConfig           `yaml:"sync"`
	Log            LogConfig            `yaml:"log"`
	Health         HealthConfig         `yaml:"health"`
	EventBus       EventBusConfig       `yaml:"eventbus"`
}

// DatabaseConfig 数据库配置（当前仅支持 sqlite）。
type DatabaseConfig struct {
	Type            string        `yaml:"type"`
	Path            string        `yaml:"path"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// SecurityConfig 安全配置（API 凭证加解密密钥）。
type SecurityConfig struct {
	EncryptionKey string `yaml:"encryption_key"`
}

// ControlGatewayConfig 配置 trade 调用 admin 网关的后台服务地址。
type ControlGatewayConfig struct {
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

// SyncConfig 定时同步配置。
type SyncConfig struct {
	Enabled          bool     `yaml:"enabled"`
	SyncBalances     bool     `yaml:"sync_balances"`
	SpaceIDs         []string `yaml:"space_ids"`
	SyncPositions    bool     `yaml:"sync_positions"`
	SyncOrders       bool     `yaml:"sync_orders"`
	SyncTrades       bool     `yaml:"sync_trades"`
	WindowHours      int      `yaml:"window_hours"`
	PageSize         int      `yaml:"page_size"`
	MaxSymbolsPerRun int      `yaml:"max_symbols_per_run"`
}

// LogConfig 日志配置。
type LogConfig struct {
	Level      string `yaml:"level"`
	OutputPath string `yaml:"output_path"`
	MaxSize    int    `yaml:"max_size"`
	MaxBackups int    `yaml:"max_backups"`
	MaxAge     int    `yaml:"max_age"`
}

// HealthConfig controls the lightweight HTTP health endpoint.
type HealthConfig struct {
	Addr string `yaml:"addr"`
}
type EventBusConfig struct {
	Enabled           bool     `yaml:"enabled"`
	URLs              []string `yaml:"urls"`
	CredentialFile    string   `yaml:"credential_file"`
	RebalanceConsumer string   `yaml:"-"`
}

const RebalanceConsumer = "trade_rebalance_v1"

// DefaultConfig 返回默认配置。
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/moox_trade.db",
			MaxIdleConns:    10,
			MaxOpenConns:    100,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Security: SecurityConfig{
			EncryptionKey: "moox-cloud-secret-key-32bytes",
		},
		ControlGateway: ControlGatewayConfig{
			BaseURL: "https://106.53.107.122:11001",
			ServiceAuth: ServiceAuthConfig{
				AccessKey:     "moox-service",
				SecretKey:     "",
				ExpireSeconds: 60,
			},
		},
		Sync: SyncConfig{
			Enabled:          true,
			SyncBalances:     true,
			SpaceIDs:         []string{"crypto"},
			SyncPositions:    true,
			SyncOrders:       true,
			SyncTrades:       true,
			WindowHours:      24,
			PageSize:         500,
			MaxSymbolsPerRun: 10,
		},
		Log: LogConfig{
			Level:      "info",
			OutputPath: "./log/moox_trade.log",
			MaxSize:    100,
			MaxBackups: 10,
			MaxAge:     30,
		},
		Health: HealthConfig{
			Addr: ":11210",
		},
		EventBus: EventBusConfig{Enabled: true, URLs: []string{"nats://127.0.0.1:4222"}, RebalanceConsumer: RebalanceConsumer},
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
		} else if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func (c *AppConfig) applyEnv() {
	if v := os.Getenv("MOOX_TRADE_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_TRADE_ENCRYPTION_KEY"); v != "" {
		c.Security.EncryptionKey = v
	}
	if v := os.Getenv("MOOX_TRADE_CONTROL_GATEWAY"); v != "" {
		c.ControlGateway.BaseURL = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"); v != "" {
		c.ControlGateway.ServiceAuth.AccessKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"); v != "" {
		c.ControlGateway.ServiceAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_NODE_ID"); v != "" {
		c.ControlGateway.ServiceAuth.TargetNode = v
	}
	if v := os.Getenv("MOOX_GATEWAY_CA_FILE"); v != "" {
		c.ControlGateway.ServiceAuth.CAFile = v
	}
	if v := os.Getenv("MOOX_TRADE_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
}

// Validate 校验配置并创建所需目录。
func (c *AppConfig) Validate() error {
	if c.Database.Type == "sqlite" {
		if c.Database.Path == "" {
			return fmt.Errorf("database path is required for SQLite")
		}
		dir := filepath.Dir(c.Database.Path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create database directory: %w", err)
		}
	}
	if c.Log.OutputPath != "" {
		if err := os.MkdirAll(filepath.Dir(c.Log.OutputPath), 0o755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
	}
	if c.Sync.WindowHours <= 0 {
		c.Sync.WindowHours = 24
	}
	if c.Sync.PageSize <= 0 || c.Sync.PageSize > 1000 {
		c.Sync.PageSize = 500
	}
	if c.Sync.MaxSymbolsPerRun <= 0 {
		c.Sync.MaxSymbolsPerRun = 10
	}
	if c.Sync.Enabled && c.Sync.SyncBalances {
		spaces := make([]string, 0, len(c.Sync.SpaceIDs))
		seen := map[string]struct{}{}
		for _, spaceID := range c.Sync.SpaceIDs {
			spaceID = strings.TrimSpace(spaceID)
			if spaceID == "" {
				continue
			}
			if _, ok := seen[spaceID]; ok {
				continue
			}
			seen[spaceID] = struct{}{}
			spaces = append(spaces, spaceID)
		}
		if len(spaces) == 0 {
			return fmt.Errorf("sync space_ids are required when balance sync is enabled")
		}
		c.Sync.SpaceIDs = spaces
	}
	if c.Health.Addr == "" {
		c.Health.Addr = ":11210"
	}
	if c.EventBus.Enabled {
		if len(c.EventBus.URLs) == 0 {
			return fmt.Errorf("eventbus urls are required")
		}
		if c.EventBus.RebalanceConsumer == "" {
			return fmt.Errorf("eventbus rebalance consumer is required")
		}
	}
	return nil
}
