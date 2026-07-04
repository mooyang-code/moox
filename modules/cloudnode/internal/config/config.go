// Package config loads moox-cloudnode process configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root moox-cloudnode configuration.
type Config struct {
	Database   DatabaseConfig   `yaml:"database"`
	JobItem    JobItemConfig    `yaml:"job_item"`
	TencentSCF TencentSCFConfig `yaml:"tencent_scf"`
}

// DatabaseConfig describes SQLite settings.
type DatabaseConfig struct {
	Type            string        `yaml:"type"`
	Path            string        `yaml:"path"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// JobItemConfig controls the async JobItem queue.
type JobItemConfig struct {
	DefaultLimit       int   `yaml:"default_limit"`
	MaxLimit           int   `yaml:"max_limit"`
	RecoverAfterMillis int64 `yaml:"recover_after_millis"`
	DefaultMaxAttempts int   `yaml:"default_max_attempts"`
}

// TencentSCFConfig stores defaults for the Tencent SCF provider.
type TencentSCFConfig struct {
	DefaultRegion    string `yaml:"default_region"`
	DefaultNamespace string `yaml:"default_namespace"`
	DefaultRuntime   string `yaml:"default_runtime"`
}

var globalConfig *Config

// Load reads YAML config from path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyEnv()
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_CLOUDNODE_DB_PATH"); v != "" {
		c.Database.Path = v
	}
}

// Default returns safe local defaults.
func Default() *Config {
	return &Config{
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/moox_cloudnode.db",
			MaxIdleConns:    10,
			MaxOpenConns:    50,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		JobItem: JobItemConfig{
			DefaultLimit:       10,
			MaxLimit:           100,
			RecoverAfterMillis: int64(10 * time.Minute / time.Millisecond),
			DefaultMaxAttempts: 3,
		},
		TencentSCF: TencentSCFConfig{
			DefaultRegion:    "ap-guangzhou",
			DefaultNamespace: "default",
			DefaultRuntime:   "Go1",
		},
	}
}

// SetGlobalConfig stores the process-wide config.
func SetGlobalConfig(cfg *Config) {
	globalConfig = cfg
}

// Global returns the process-wide config.
func Global() *Config {
	if globalConfig == nil {
		globalConfig = Default()
	}
	return globalConfig
}
