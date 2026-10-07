// Package config loads moox-cloudnode process configuration.
package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root moox-cloudnode configuration.
type Config struct {
	Database   DatabaseConfig   `yaml:"database"`
	NodeBatch  NodeBatchConfig  `yaml:"node_batch"`
	TencentSCF TencentSCFConfig `yaml:"tencent_scf"`
	Debug      DebugConfig      `yaml:"debug"`
	Health     HealthConfig     `yaml:"health"`
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

// NodeBatchConfig controls asynchronous SCF node batch execution.
type NodeBatchConfig struct {
	BatchSize    int           `yaml:"batch_size"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

// TencentSCFConfig stores defaults for the Tencent SCF provider.
type TencentSCFConfig struct {
	DefaultRegion    string `yaml:"default_region"`
	DefaultNamespace string `yaml:"default_namespace"`
	DefaultRuntime   string `yaml:"default_runtime"`
}

// DebugConfig controls local diagnostics endpoints.
type DebugConfig struct {
	PprofAddr string `yaml:"pprof_addr"`
}

// HealthConfig controls the lightweight HTTP health endpoint.
type HealthConfig struct {
	Addr string `yaml:"addr"`
}

// Load reads YAML config from path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is required")
	}
	if c.NodeBatch.BatchSize < 1 || c.NodeBatch.BatchSize > 10 {
		return fmt.Errorf("node_batch.batch_size must be between 1 and 10")
	}
	if c.NodeBatch.PollInterval < 100*time.Millisecond || c.NodeBatch.PollInterval > 10*time.Second {
		return fmt.Errorf("node_batch.poll_interval must be between 100ms and 10s")
	}
	return nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_CLOUDNODE_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_CLOUDNODE_PPROF_ADDR"); v != "" {
		c.Debug.PprofAddr = v
	}
	if v := os.Getenv("MOOX_CLOUDNODE_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
}

// Default returns safe local defaults.
func Default() *Config {
	return &Config{
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/moox_cloudnode.db",
			MaxIdleConns:    1,
			MaxOpenConns:    1,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		NodeBatch: NodeBatchConfig{
			BatchSize:    3,
			PollInterval: 500 * time.Millisecond,
		},
		TencentSCF: TencentSCFConfig{
			DefaultRegion:    "ap-guangzhou",
			DefaultNamespace: "default",
			DefaultRuntime:   "Go1",
		},
		Debug: DebugConfig{},
		Health: HealthConfig{
			Addr: ":11411",
		},
	}
}
