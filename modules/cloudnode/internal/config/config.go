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
	Queue      QueueConfig      `yaml:"queue"`
	JetStream  JetStreamConfig  `yaml:"jetstream"`
	JobItem    JobItemConfig    `yaml:"job_item"`
	TencentSCF TencentSCFConfig `yaml:"tencent_scf"`
	Debug      DebugConfig      `yaml:"debug"`
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
	DefaultLimit         int    `yaml:"default_limit"`
	MaxLimit             int    `yaml:"max_limit"`
	RecoverAfterMillis   int64  `yaml:"recover_after_millis"`
	DefaultMaxAttempts   int    `yaml:"default_max_attempts"`
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
	Enabled        bool                    `yaml:"enabled"`
	NATSURL        string                  `yaml:"nats_url"`
	SubjectPrefix  string                  `yaml:"subject_prefix"`
	ExecStream     string                  `yaml:"exec_stream"`
	Embedded       EmbeddedJetStreamConfig `yaml:"embedded"`
	AckWaitMillis  int64                   `yaml:"ack_wait_millis"`
	MaxDeliver     int                     `yaml:"max_deliver"`
	FetchMaxWaitMs int64                   `yaml:"fetch_max_wait_ms"`
}

// EmbeddedJetStreamConfig starts a local private NATS JetStream for CloudNode.
type EmbeddedJetStreamConfig struct {
	Enabled          bool   `yaml:"enabled"`
	Host             string `yaml:"host"`
	Port             int    `yaml:"port"`
	StoreDir         string `yaml:"store_dir"`
	StartupTimeoutMS int64  `yaml:"startup_timeout_ms"`
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
	if v := os.Getenv("MOOX_CLOUDNODE_PPROF_ADDR"); v != "" {
		c.Debug.PprofAddr = v
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
		Queue: QueueConfig{
			Backend: "jetstream",
		},
		JetStream: JetStreamConfig{
			Enabled:        true,
			NATSURL:        "nats://127.0.0.1:4223",
			SubjectPrefix:  "moox.cloudnode",
			ExecStream:     "MOOX_CLOUDNODE_EXEC",
			AckWaitMillis:  int64(2 * time.Minute / time.Millisecond),
			MaxDeliver:     3,
			FetchMaxWaitMs: 500,
			Embedded: EmbeddedJetStreamConfig{
				Enabled:          true,
				Host:             "127.0.0.1",
				Port:             4223,
				StoreDir:         "../data/cloudnode/nats",
				StartupTimeoutMS: 10000,
			},
		},
		JobItem: JobItemConfig{
			DefaultLimit:         10,
			MaxLimit:             100,
			RecoverAfterMillis:   int64(10 * time.Minute / time.Millisecond),
			DefaultMaxAttempts:   3,
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
