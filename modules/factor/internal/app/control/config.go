// Package control wires the moox-factor service process.
package control

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root factor service configuration.
type Config struct {
	Database  DatabaseConfig  `yaml:"database"`
	Storage   StorageConfig   `yaml:"storage"`
	NATS      NATSConfig      `yaml:"nats"`
	Engine    EngineConfig    `yaml:"engine"`
	Scheduler SchedulerConfig `yaml:"scheduler"`
	Instance  InstanceConfig  `yaml:"instance"`
	SysDeploy SysDeployConfig `yaml:"sysdeploy"`
	Health    HealthConfig    `yaml:"health"`
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
	MetadataTarget string `yaml:"metadata_target"`
	AccessTarget   string `yaml:"access_target"`
}

// NATSConfig describes the Storage event stream subscription.
type NATSConfig struct {
	URL      string `yaml:"url"`
	Stream   string `yaml:"stream"`
	Consumer string `yaml:"consumer"`
	Subject  string `yaml:"subject"`
}

// EngineConfig describes the local Python factor engine.
type EngineConfig struct {
	PythonBin         string `yaml:"python_bin"`
	FactorsDir        string `yaml:"factors_dir"`
	SectionsDir       string `yaml:"sections_dir"`
	Workers           int    `yaml:"workers"`
	TaskTimeoutMS     int    `yaml:"task_timeout_ms"`
	Encoding          string `yaml:"encoding"`
	ArrowRowThreshold int    `yaml:"arrow_row_threshold"`
	ShmDir            string `yaml:"shm_dir"`
}

// SchedulerConfig describes runtime scheduling behavior.
type SchedulerConfig struct {
	DebounceWindowMS     int `yaml:"debounce_window_ms"`
	MaxRetry             int `yaml:"max_retry"`
	ReconcileIntervalMin int `yaml:"reconcile_interval_min"`
}

// InstanceConfig identifies a factor process in multi-instance deployments.
type InstanceConfig struct {
	InstanceID          string `yaml:"instance_id"`
	Role                string `yaml:"role"`
	PrimaryTarget       string `yaml:"primary_target"`
	HeartbeatIntervalMS int    `yaml:"heartbeat_interval_ms"`
}

// SysDeployConfig describes optional dependency discovery through admin.
type SysDeployConfig struct {
	AdminGatewayURL string            `yaml:"admin_gateway_url"`
	ServiceAuth     ServiceAuthConfig `yaml:"service_auth"`
}

// ServiceAuthConfig describes backend HMAC auth for service calls.
type ServiceAuthConfig struct {
	Version       string `yaml:"version"`
	AccessKey     string `yaml:"access_key"`
	SecretKey     string `yaml:"secret_key"`
	ExpireSeconds int64  `yaml:"expire_seconds"`
}

// HealthConfig controls the lightweight HTTP health endpoint.
type HealthConfig struct {
	Addr string `yaml:"addr"`
}

var globalConfig *Config

// Load reads YAML config from path and applies factor-specific env overrides.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
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
			MetadataTarget: "127.0.0.1:20100",
			AccessTarget:   "127.0.0.1:20102",
		},
		NATS: NATSConfig{
			URL:      "nats://127.0.0.1:4222",
			Stream:   "MOOX_STORAGE",
			Consumer: "factor_calc",
			Subject:  "moox.storage.time_series.rows_changed.v1",
		},
		Engine: EngineConfig{
			PythonBin:         "python3",
			FactorsDir:        "./factors",
			SectionsDir:       "./sections",
			Workers:           workers,
			TaskTimeoutMS:     30000,
			Encoding:          "auto",
			ArrowRowThreshold: 50000,
		},
		Scheduler: SchedulerConfig{
			DebounceWindowMS:     2000,
			MaxRetry:             3,
			ReconcileIntervalMin: 10,
		},
		Instance: InstanceConfig{
			InstanceID:          "factor-01",
			Role:                "primary",
			HeartbeatIntervalMS: 5000,
		},
		SysDeploy: SysDeployConfig{
			AdminGatewayURL: "http://127.0.0.1:11000",
			ServiceAuth: ServiceAuthConfig{
				Version:       "moox-auth-v1",
				ExpireSeconds: 1800,
			},
		},
		Health: HealthConfig{
			Addr: ":11414",
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
	if c.Storage.MetadataTarget == "" {
		c.Storage.MetadataTarget = "127.0.0.1:20100"
	}
	if c.Storage.AccessTarget == "" {
		c.Storage.AccessTarget = "127.0.0.1:20102"
	}
	if c.NATS.URL == "" {
		c.NATS.URL = "nats://127.0.0.1:4222"
	}
	if c.NATS.Stream == "" {
		c.NATS.Stream = "MOOX_STORAGE"
	}
	if c.NATS.Consumer == "" {
		c.NATS.Consumer = "factor_calc"
	}
	if c.NATS.Subject == "" {
		c.NATS.Subject = "moox.storage.time_series.rows_changed.v1"
	}
	if c.Engine.PythonBin == "" {
		c.Engine.PythonBin = "python3"
	}
	if c.Engine.FactorsDir == "" {
		c.Engine.FactorsDir = "./factors"
	}
	if c.Engine.SectionsDir == "" {
		c.Engine.SectionsDir = "./sections"
	}
	if c.Engine.Workers <= 0 {
		c.Engine.Workers = defaultWorkerCount()
	}
	if c.Engine.TaskTimeoutMS == 0 {
		c.Engine.TaskTimeoutMS = 30000
	}
	if c.Engine.Encoding == "" {
		c.Engine.Encoding = "auto"
	}
	if c.Engine.ArrowRowThreshold == 0 {
		c.Engine.ArrowRowThreshold = 50000
	}
	if c.Scheduler.DebounceWindowMS == 0 {
		c.Scheduler.DebounceWindowMS = 2000
	}
	if c.Scheduler.MaxRetry == 0 {
		c.Scheduler.MaxRetry = 3
	}
	if c.Scheduler.ReconcileIntervalMin == 0 {
		c.Scheduler.ReconcileIntervalMin = 10
	}
	if c.Instance.InstanceID == "" {
		c.Instance.InstanceID = "factor-01"
	}
	if c.Instance.Role == "" {
		c.Instance.Role = "primary"
	}
	if c.Instance.HeartbeatIntervalMS == 0 {
		c.Instance.HeartbeatIntervalMS = 5000
	}
	if c.SysDeploy.AdminGatewayURL == "" {
		c.SysDeploy.AdminGatewayURL = "http://127.0.0.1:11000"
	}
	if c.SysDeploy.ServiceAuth.Version == "" {
		c.SysDeploy.ServiceAuth.Version = "moox-auth-v1"
	}
	if c.SysDeploy.ServiceAuth.ExpireSeconds == 0 {
		c.SysDeploy.ServiceAuth.ExpireSeconds = 1800
	}
	if c.Health.Addr == "" {
		c.Health.Addr = ":11414"
	}
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_FACTOR_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_FACTOR_STORAGE_METADATA_TARGET"); v != "" {
		c.Storage.MetadataTarget = v
	}
	if v := os.Getenv("MOOX_FACTOR_STORAGE_ACCESS_TARGET"); v != "" {
		c.Storage.AccessTarget = v
	}
	if v, ok := os.LookupEnv("MOOX_FACTOR_NATS_URL"); ok {
		c.NATS.URL = v
	}
	if v := os.Getenv("MOOX_FACTOR_ENGINE_PYTHON_BIN"); v != "" {
		c.Engine.PythonBin = v
	}
	if v := os.Getenv("MOOX_FACTOR_ENGINE_FACTORS_DIR"); v != "" {
		c.Engine.FactorsDir = v
	}
	if v := os.Getenv("MOOX_FACTOR_ENGINE_SECTIONS_DIR"); v != "" {
		c.Engine.SectionsDir = v
	}
	if v := os.Getenv("MOOX_FACTOR_ENGINE_WORKERS"); v != "" {
		if workers, err := strconv.Atoi(v); err == nil && workers > 0 {
			c.Engine.Workers = workers
		}
	}
	if v := os.Getenv("MOOX_FACTOR_ADMIN_GATEWAY_URL"); v != "" {
		c.SysDeploy.AdminGatewayURL = v
	}
	if v := os.Getenv("MOOX_FACTOR_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
	if v := os.Getenv("MOOX_SERVICE_AUTH_VERSION"); v != "" {
		c.SysDeploy.ServiceAuth.Version = v
	}
	if v := os.Getenv("MOOX_SERVICE_AUTH_ACCESS_KEY"); v != "" {
		c.SysDeploy.ServiceAuth.AccessKey = v
	}
	if v := os.Getenv("MOOX_SERVICE_AUTH_SECRET_KEY"); v != "" {
		c.SysDeploy.ServiceAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_SERVICE_AUTH_EXPIRE_SECONDS"); v != "" {
		if seconds, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.SysDeploy.ServiceAuth.ExpireSeconds = seconds
		}
	}
}

func (c *Config) validateStorageTargets() error {
	if !isTRPCTarget(c.Storage.MetadataTarget) {
		return fmt.Errorf("storage.metadata_target must be a tRPC target, got %q", c.Storage.MetadataTarget)
	}
	if !isTRPCTarget(c.Storage.AccessTarget) {
		return fmt.Errorf("storage.access_target must be a tRPC target, got %q", c.Storage.AccessTarget)
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

// SetGlobalConfig stores the process-wide factor config.
func SetGlobalConfig(cfg *Config) {
	globalConfig = cfg
}

// Global returns the process-wide factor config.
func Global() *Config {
	if globalConfig == nil {
		globalConfig = Default()
	}
	return globalConfig
}
