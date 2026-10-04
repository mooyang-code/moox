// Package config 提供统一的配置管理
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// AppConfig 应用配置（总配置）
type AppConfig struct {
	Database DatabaseConfig `yaml:"database"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Path            string        `yaml:"path"`               // SQLite文件路径
	MaxIdleConns    int           `yaml:"max_idle_conns"`     // 最大空闲连接数
	MaxOpenConns    int           `yaml:"max_open_conns"`     // 最大打开连接数
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`  // 连接最大生命周期
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"` // 连接最大空闲时间
}

// DefaultConfig 返回默认配置
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Database: DatabaseConfig{
			Path:            "./data/admin.db",
			MaxIdleConns:    4,
			MaxOpenConns:    8,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
	}
}

// Load 从文件加载配置
func Load(configPath string) (*AppConfig, error) {
	// 1. 加载默认配置
	cfg := DefaultConfig()

	// 2. 如果配置文件存在，从文件加载
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("failed to read config file: %w", err)
			}
			// 文件不存在，使用默认配置
		} else {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("failed to parse config file: %w", err)
			}
		}
	}

	// 3. 从环境变量覆盖
	cfg.applyEnv()

	// 4. 验证配置
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// applyEnv 从环境变量覆盖配置
func (c *AppConfig) applyEnv() {
	// Database
	if v := os.Getenv("MOOX_ADMIN_DB_PATH"); v != "" {
		c.Database.Path = v
	}

}

// Validate 验证配置
func (c *AppConfig) Validate() error {
	// 验证必填项

	if c.Database.Path == "" {
		return fmt.Errorf("database path is required")
	}
	// 确保目录存在
	dir := filepath.Dir(c.Database.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	return nil
}
