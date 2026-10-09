// Package config 提供 Trade 模块的应用配置加载。
//
// Trade 模块使用独立的 SQLite 库（账户域 + 交易域同库）。
// trpc_go.yaml 由 trpc-go 运行时自动加载，本包只加载业务侧 app.yaml。
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// AppConfig Trade 应用配置。
type AppConfig struct {
	Database DatabaseConfig `yaml:"database"`
	// GatewayClient 是 Trade 调用 Admin SecretMgr 使用的 gatewayclient 配置（trade 身份）。
	GatewayClient gatewayclient.Config `yaml:"gateway_client"`
	EventBus      EventBusConfig       `yaml:"eventbus"`
	Runtime       RuntimeConfig        `yaml:"runtime"`
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
		Runtime: RuntimeConfig{},
		// 与部署布局一致：密钥和 CA 在安装根目录，目录缓存在组件的数据目录。
		GatewayClient: gatewayclient.Config{
			Mode: gatewayclient.ModeLocal, Caller: "trade", KeyFile: "../secrets/caller-trade.key",
			CAFile: "../certs/moox-ca.crt", CacheDir: "./data/gatewayclient",
		},
		EventBus: EventBusConfig{Enabled: true, URLs: []string{"nats://127.0.0.1:4222"}, TargetConsumer: TargetConsumer},
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
	return c.GatewayClient.Validate()
}
