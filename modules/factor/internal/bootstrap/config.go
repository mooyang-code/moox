// Package bootstrap loads configuration and wires the moox-factor service process.
package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"gopkg.in/yaml.v3"
)

// Config is the root factor service configuration.
type Config struct {
	Database  DatabaseConfig  `yaml:"database"`
	Storage   StorageConfig   `yaml:"storage"`
	EventBus  EventBusConfig  `yaml:"eventbus"`
	Engine    EngineConfig    `yaml:"engine"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
}

// DatabaseConfig describes local SQLite settings.
type DatabaseConfig struct {
	Type            string        `yaml:"type"`
	Path            string        `yaml:"path"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// StorageConfig describes storage service tRPC targets.
type StorageConfig struct {
	GatewayTarget string `yaml:"gateway_target"`
	GatewayNodeID string `yaml:"gateway_node_id"`
	KeyID         string `yaml:"key_id"`
	HMACKeyFile   string `yaml:"hmac_key_file"`
}

// EventBusConfig describes the Storage event stream subscription.
type EventBusConfig struct {
	URLs           []string      `yaml:"urls"`
	FetchMaxWait   time.Duration `yaml:"fetch_max_wait"`
	CredentialFile string        `yaml:"credential_file"`
}

// EngineConfig describes the local Python factor engine.
type EngineConfig struct {
	PythonBin     string `yaml:"python_bin"`
	WorkerPath    string `yaml:"worker_path"`
	FactorsDir    string `yaml:"factors_dir"`
	Workers       int    `yaml:"workers"`
	TaskTimeoutMS int    `yaml:"task_timeout_ms"`
}

// SchedulerConfig describes runtime scheduling behavior.
type SchedulerConfig struct {
	EventBatchWindowMS int `yaml:"event_batch_window_ms"`
	QueueCapacity      int `yaml:"queue_capacity"`
	MaxRetry           int `yaml:"max_retry"`
}

// Load reads YAML config from path and applies factor-specific env overrides.
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
	cfg.applyDefaults()
	cfg.applyEnv()
	if err := cfg.validateStorageTargets(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Default returns safe local development defaults.
func Default() *Config {
	workers := defaultWorkerCount()
	return &Config{
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/factor/factor.db",
			MaxIdleConns:    10,
			MaxOpenConns:    30,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Storage: StorageConfig{
			GatewayTarget: "ip://127.0.0.1:11003",
		},
		EventBus: EventBusConfig{
			URLs:         []string{"nats://127.0.0.1:4222"},
			FetchMaxWait: time.Second,
		},
		Engine: EngineConfig{
			PythonBin: "python3", WorkerPath: "./pyworker/worker.py", FactorsDir: "./factors",
			Workers: workers, TaskTimeoutMS: 30000,
		},
		Scheduler: SchedulerConfig{
			EventBatchWindowMS: 2000, QueueCapacity: 2048, MaxRetry: 1,
		},
	}
}

func (c *Config) applyDefaults() {
	if c.Database.Type == "" {
		c.Database.Type = "sqlite"
	}
	if c.Database.Path == "" {
		c.Database.Path = "./data/factor/factor.db"
	}
	if c.Database.MaxIdleConns == 0 {
		c.Database.MaxIdleConns = 10
	}
	if c.Database.MaxOpenConns == 0 {
		c.Database.MaxOpenConns = 30
	}
	if c.Database.ConnMaxLifetime == 0 {
		c.Database.ConnMaxLifetime = time.Hour
	}
	if c.Database.ConnMaxIdleTime == 0 {
		c.Database.ConnMaxIdleTime = 10 * time.Minute
	}
	if c.Storage.GatewayTarget == "" {
		c.Storage.GatewayTarget = "ip://127.0.0.1:11003"
	}
	if len(c.EventBus.URLs) == 0 {
		c.EventBus.URLs = []string{"nats://127.0.0.1:4222"}
	}
	if c.EventBus.FetchMaxWait == 0 {
		c.EventBus.FetchMaxWait = time.Second
	}
	if c.Engine.PythonBin == "" {
		c.Engine.PythonBin = "python3"
	}
	if c.Engine.WorkerPath == "" {
		c.Engine.WorkerPath = "./pyworker/worker.py"
	}
	if c.Engine.FactorsDir == "" {
		c.Engine.FactorsDir = "./factors"
	}
	if c.Engine.Workers <= 0 {
		c.Engine.Workers = defaultWorkerCount()
	}
	if c.Engine.TaskTimeoutMS == 0 {
		c.Engine.TaskTimeoutMS = 30000
	}
	if c.Scheduler.EventBatchWindowMS == 0 {
		c.Scheduler.EventBatchWindowMS = 2000
	}
	if c.Scheduler.QueueCapacity <= 0 {
		c.Scheduler.QueueCapacity = 2048
	}
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_FACTOR_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET"); v != "" {
		c.Storage.GatewayTarget = v
	}
	if v := os.Getenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID"); v != "" {
		c.Storage.GatewayNodeID = v
	}
	if v := os.Getenv("MOOX_FACTOR_STORAGE_RPC_KEY_ID"); v != "" {
		c.Storage.KeyID = v
	}
	if v := os.Getenv("MOOX_FACTOR_STORAGE_RPC_HMAC_KEY_FILE"); v != "" {
		c.Storage.HMACKeyFile = v
	}
	if v, ok := os.LookupEnv("MOOX_EVENTBUS_NATS_URL"); ok {
		c.EventBus.URLs = splitEventBusURLs(v)
	}
	if v := os.Getenv("MOOX_FACTOR_ENGINE_PYTHON_BIN"); v != "" {
		c.Engine.PythonBin = v
	}
	if v := os.Getenv("MOOX_FACTOR_ENGINE_WORKER_PATH"); v != "" {
		c.Engine.WorkerPath = v
	}
	if v := os.Getenv("MOOX_FACTOR_ENGINE_FACTORS_DIR"); v != "" {
		c.Engine.FactorsDir = v
	}
	if v := os.Getenv("MOOX_FACTOR_ENGINE_WORKERS"); v != "" {
		if workers, err := strconv.Atoi(v); err == nil && workers > 0 {
			c.Engine.Workers = workers
		}
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

func (c *Config) validateStorageTargets() error {
	if c.Scheduler.MaxRetry < 0 {
		return fmt.Errorf("scheduler.max_retry must be non-negative")
	}
	if !isTRPCTarget(c.Storage.GatewayTarget) {
		return fmt.Errorf("storage.gateway_target must be a tRPC target, got %q", c.Storage.GatewayTarget)
	}
	if strings.TrimSpace(c.Storage.HMACKeyFile) != "" {
		if _, err := gatewayauth.CredentialsFromKeyFile(c.Storage.KeyID, c.Storage.HMACKeyFile); err != nil {
			return fmt.Errorf("storage hmac credentials: %w", err)
		}
	}
	return nil
}

func isTRPCTarget(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	return raw != "" && !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://")
}

func defaultWorkerCount() int {
	n := runtime.NumCPU()
	if n <= 0 {
		return 1
	}
	if n > 8 {
		return 8
	}
	return n
}
