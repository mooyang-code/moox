// Package config loads moox-cloudnode process configuration.
package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root moox-cloudnode configuration.
type Config struct {
	Database   DatabaseConfig   `yaml:"database"`
	Queue      QueueConfig      `yaml:"queue"`
	JetStream  JetStreamConfig  `yaml:"jetstream"`
	JobItem    JobItemConfig    `yaml:"job_item"`
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

// JobItemConfig controls the async JobItem queue.
type JobItemConfig struct {
	ActiveKVBucket       string `yaml:"active_kv_bucket"`
	ActiveTTLHours       int    `yaml:"active_ttl_hours"`
	HistoryDir           string `yaml:"history_dir"`
	HistoryRetentionDays int    `yaml:"history_retention_days"`
}

// QueueConfig selects the CloudNode JobItem execution queue backend.
type QueueConfig struct {
	Backend string `yaml:"backend"`
}

// JetStreamConfig controls the CloudNode JetStream execution queue.
type JetStreamConfig struct {
	Enabled        bool     `yaml:"enabled"`
	URLs           []string `yaml:"urls"`
	CredentialFile string   `yaml:"credential_file"`
	MaxDeliver     int      `yaml:"max_deliver"`
	MaxAckPending  int      `yaml:"max_ack_pending"`
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
	if c.Queue.Backend == "jetstream" && c.JetStream.Enabled && c.JetStream.MaxDeliver <= 0 {
		return fmt.Errorf("jetstream.max_deliver must be positive")
	}
	if c.Queue.Backend == "jetstream" && c.JetStream.Enabled && c.JetStream.MaxAckPending <= 0 {
		return fmt.Errorf("jetstream.max_ack_pending must be positive")
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
	if v := os.Getenv("MOOX_EVENTBUS_NATS_URL"); v != "" {
		c.JetStream.URLs = splitEventBusURLs(v)
	}
}

func splitEventBusURLs(value string) []string {
	parts := strings.Split(value, ",")
	urls := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			urls = append(urls, trimmed)
		}
	}
	return urls
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
		Queue: QueueConfig{
			Backend: "jetstream",
		},
		JetStream: JetStreamConfig{
			Enabled:        true,
			URLs:           []string{"nats://127.0.0.1:4222"},
			CredentialFile: "~/.config/moox/eventbus/cloudnode-eventbus.yaml",
			MaxDeliver:     3,
			MaxAckPending:  32,
		},
		JobItem: JobItemConfig{
			ActiveKVBucket:       "MOOX_CLOUDNODE_JOB_ACTIVE",
			ActiveTTLHours:       48,
			HistoryDir:           "../data/cloudnode/jobs",
			HistoryRetentionDays: 2,
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
