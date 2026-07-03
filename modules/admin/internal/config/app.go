// Package config 提供统一的配置管理
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// 全局配置实例
var (
	globalConfig *AppConfig
	configMutex  sync.RWMutex
)

// AppConfig 应用配置（总配置）
type AppConfig struct {
	Database DatabaseConfig `yaml:"database"`
	Monitor  MonitorConfig  `yaml:"monitor"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Path            string        `yaml:"path"`               // SQLite文件路径
	MaxIdleConns    int           `yaml:"max_idle_conns"`     // 最大空闲连接数
	MaxOpenConns    int           `yaml:"max_open_conns"`     // 最大打开连接数
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`  // 连接最大生命周期
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"` // 连接最大空闲时间
}

// MonitorConfig 监控配置
type MonitorConfig struct {
	NodeExporterPort int `yaml:"node_exporter_port"` // Node Exporter 端口，默认 9100
	CollectTimeout   int `yaml:"collect_timeout"`    // 采集超时时间（秒），默认 10
	ConcurrentLimit  int `yaml:"concurrent_limit"`   // 并发采集限制，默认 20
}

// DefaultConfig 返回默认配置
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Database: DatabaseConfig{
			Path:            "./data/admin.db",
			MaxIdleConns:    10,
			MaxOpenConns:    100,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Monitor: MonitorConfig{
			NodeExporterPort: 9100, // Node Exporter 默认端口
			CollectTimeout:   10,   // 10秒超时
			ConcurrentLimit:  20,   // 最多 20 个并发
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

	// Monitor
	if v := os.Getenv("NODE_EXPORTER_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Monitor.NodeExporterPort)
	}
	if v := os.Getenv("MONITOR_COLLECT_TIMEOUT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Monitor.CollectTimeout)
	}
	if v := os.Getenv("MONITOR_CONCURRENT_LIMIT"); v != "" {
		fmt.Sscanf(v, "%d", &c.Monitor.ConcurrentLimit)
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

// SetGlobalConfig 设置全局配置（由 bootstrap 在启动时调用）
func SetGlobalConfig(cfg *AppConfig) {
	configMutex.Lock()
	defer configMutex.Unlock()
	globalConfig = cfg
}

// GetGlobalConfig 获取全局配置
func GetGlobalConfig() *AppConfig {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return globalConfig
}

// GetMonitorConfig 获取监控配置
func GetMonitorConfig() MonitorConfig {
	cfg := GetGlobalConfig()
	if cfg == nil {
		// 返回默认配置
		return MonitorConfig{
			NodeExporterPort: 9100,
			CollectTimeout:   10,
			ConcurrentLimit:  20,
		}
	}
	return cfg.Monitor
}
