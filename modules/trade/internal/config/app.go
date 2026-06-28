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
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	globalConfig *AppConfig
	configMutex  sync.RWMutex
)

// AppConfig Trade 应用配置。
type AppConfig struct {
	Database DatabaseConfig `yaml:"database"`
	Security SecurityConfig `yaml:"security"`
	Log      LogConfig      `yaml:"log"`
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

// LogConfig 日志配置。
type LogConfig struct {
	Level      string `yaml:"level"`
	OutputPath string `yaml:"output_path"`
	MaxSize    int    `yaml:"max_size"`
	MaxBackups int    `yaml:"max_backups"`
	MaxAge     int    `yaml:"max_age"`
}

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
		Log: LogConfig{
			Level:      "info",
			OutputPath: "./log/moox_trade.log",
			MaxSize:    100,
			MaxBackups: 10,
			MaxAge:     30,
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
	if v := os.Getenv("DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_ENCRYPTION_KEY"); v != "" {
		c.Security.EncryptionKey = v
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
	return nil
}

// SetGlobalConfig 设置全局配置（bootstrap 启动时调用）。
func SetGlobalConfig(cfg *AppConfig) {
	configMutex.Lock()
	defer configMutex.Unlock()
	globalConfig = cfg
}

// GetGlobalConfig 获取全局配置。
func GetGlobalConfig() *AppConfig {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return globalConfig
}
