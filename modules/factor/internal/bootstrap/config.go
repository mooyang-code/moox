// Package bootstrap loads configuration and wires the moox-factor process.
package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Database DatabaseConfig `yaml:"database"`
	Storage  StorageConfig  `yaml:"storage"`
	EventBus EventBusConfig `yaml:"eventbus"`
	Python   PythonConfig   `yaml:"python"`
	Pipeline PipelineConfig `yaml:"pipeline"`
	Recalc   RecalcConfig   `yaml:"recalc"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type StorageConfig struct {
	GatewayTarget string `yaml:"gateway_target"`
	GatewayNodeID string `yaml:"gateway_node_id"`
	KeyID         string `yaml:"key_id"`
	HMACKeyFile   string `yaml:"hmac_key_file"`
}

type EventBusConfig struct {
	URLs           []string      `yaml:"urls"`
	CredentialFile string        `yaml:"credential_file"`
	FetchMaxWait   time.Duration `yaml:"fetch_max_wait"`
}

type PythonConfig struct {
	Bin         string        `yaml:"bin"`
	WorkerPath  string        `yaml:"worker_path"`
	FactorsDir  string        `yaml:"factors_dir"`
	Workers     int           `yaml:"workers"`
	TaskTimeout time.Duration `yaml:"task_timeout"`
}

type PipelineConfig struct {
	ReadBatchSubjects int           `yaml:"read_batch_subjects"`
	ReadWorkers       int           `yaml:"read_workers"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteBatchRows    int           `yaml:"write_batch_rows"`
	PeriodBudgetMin   time.Duration `yaml:"period_budget_min"`
	PeriodBudgetMax   time.Duration `yaml:"period_budget_max"`
}

type RecalcConfig struct {
	ChunkPeriods int `yaml:"chunk_periods"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read factor config %s: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse factor config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Default() *Config {
	return &Config{
		Database: DatabaseConfig{Path: "./data/factor/factor.db"},
		Storage:  StorageConfig{GatewayTarget: "ip://127.0.0.1:11003", KeyID: "factor"},
		EventBus: EventBusConfig{
			URLs:           []string{"nats://127.0.0.1:4222"},
			CredentialFile: "~/.config/moox/eventbus/factor-eventbus.yaml",
			FetchMaxWait:   10 * time.Second,
		},
		Python: PythonConfig{
			Bin: "python3", WorkerPath: "./pyworker/worker.py", FactorsDir: "./data/factor/factors",
			Workers: 8, TaskTimeout: 30 * time.Second,
		},
		Pipeline: PipelineConfig{
			ReadBatchSubjects: 100, ReadWorkers: 4, ReadTimeout: 20 * time.Second,
			WriteBatchRows: 1000, PeriodBudgetMin: time.Minute, PeriodBudgetMax: 15 * time.Minute,
		},
		Recalc: RecalcConfig{ChunkPeriods: 2000},
	}
}

func (c *Config) applyDefaults() {
	defaults := Default()
	if c.Database.Path == "" {
		c.Database.Path = defaults.Database.Path
	}
	if c.Storage.GatewayTarget == "" {
		c.Storage.GatewayTarget = defaults.Storage.GatewayTarget
	}
	if c.Storage.KeyID == "" {
		c.Storage.KeyID = defaults.Storage.KeyID
	}
	if len(c.EventBus.URLs) == 0 {
		c.EventBus.URLs = defaults.EventBus.URLs
	}
	if c.EventBus.CredentialFile == "" {
		c.EventBus.CredentialFile = defaults.EventBus.CredentialFile
	}
	if c.EventBus.FetchMaxWait == 0 {
		c.EventBus.FetchMaxWait = defaults.EventBus.FetchMaxWait
	}
	if c.Python.Bin == "" {
		c.Python.Bin = defaults.Python.Bin
	}
	if c.Python.WorkerPath == "" {
		c.Python.WorkerPath = defaults.Python.WorkerPath
	}
	if c.Python.FactorsDir == "" {
		c.Python.FactorsDir = defaults.Python.FactorsDir
	}
	if c.Python.Workers == 0 {
		c.Python.Workers = defaults.Python.Workers
	}
	if c.Python.TaskTimeout == 0 {
		c.Python.TaskTimeout = defaults.Python.TaskTimeout
	}
	if c.Pipeline.ReadBatchSubjects == 0 {
		c.Pipeline.ReadBatchSubjects = defaults.Pipeline.ReadBatchSubjects
	}
	if c.Pipeline.ReadWorkers == 0 {
		c.Pipeline.ReadWorkers = defaults.Pipeline.ReadWorkers
	}
	if c.Pipeline.ReadTimeout == 0 {
		c.Pipeline.ReadTimeout = defaults.Pipeline.ReadTimeout
	}
	if c.Pipeline.WriteBatchRows == 0 {
		c.Pipeline.WriteBatchRows = defaults.Pipeline.WriteBatchRows
	}
	if c.Pipeline.PeriodBudgetMin == 0 {
		c.Pipeline.PeriodBudgetMin = defaults.Pipeline.PeriodBudgetMin
	}
	if c.Pipeline.PeriodBudgetMax == 0 {
		c.Pipeline.PeriodBudgetMax = defaults.Pipeline.PeriodBudgetMax
	}
	if c.Recalc.ChunkPeriods == 0 {
		c.Recalc.ChunkPeriods = defaults.Recalc.ChunkPeriods
	}
}

func (c *Config) applyEnv() error {
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_DB_PATH")); value != "" {
		c.Database.Path = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_GATEWAY_TARGET")); value != "" {
		c.Storage.GatewayTarget = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_GATEWAY_NODE_ID")); value != "" {
		c.Storage.GatewayNodeID = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_KEY_ID")); value != "" {
		c.Storage.KeyID = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_HMAC_KEY_FILE")); value != "" {
		c.Storage.HMACKeyFile = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_EVENTBUS_NATS_URL")); value != "" {
		c.EventBus.URLs = splitURLs(value)
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE")); value != "" {
		c.EventBus.CredentialFile = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_PYTHON_BIN")); value != "" {
		c.Python.Bin = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_PYTHON_WORKER_PATH")); value != "" {
		c.Python.WorkerPath = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_PYTHON_FACTORS_DIR")); value != "" {
		c.Python.FactorsDir = value
	}
	return nil
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("factor config is required")
	}
	if strings.TrimSpace(c.Database.Path) == "" {
		return fmt.Errorf("database.path is required")
	}
	if !validTRPCTarget(c.Storage.GatewayTarget) {
		return fmt.Errorf("storage.gateway_target must be a tRPC target")
	}
	if strings.TrimSpace(c.Storage.HMACKeyFile) != "" {
		if _, err := gatewayauth.CredentialsFromKeyFile(c.Storage.KeyID, c.Storage.HMACKeyFile); err != nil {
			return fmt.Errorf("storage hmac credentials: %w", err)
		}
	}
	if len(c.EventBus.URLs) == 0 {
		return fmt.Errorf("eventbus.urls must not be empty")
	}
	if c.EventBus.FetchMaxWait <= 0 {
		return fmt.Errorf("eventbus.fetch_max_wait must be positive")
	}
	if strings.TrimSpace(c.Python.Bin) == "" || strings.TrimSpace(c.Python.WorkerPath) == "" || strings.TrimSpace(c.Python.FactorsDir) == "" {
		return fmt.Errorf("python.bin, python.worker_path and python.factors_dir are required")
	}
	if c.Python.Workers <= 0 || c.Python.TaskTimeout <= 0 {
		return fmt.Errorf("python.workers and python.task_timeout must be positive")
	}
	if c.Pipeline.ReadBatchSubjects <= 0 || c.Pipeline.ReadWorkers <= 0 || c.Pipeline.ReadTimeout <= 0 || c.Pipeline.WriteBatchRows <= 0 {
		return fmt.Errorf("pipeline batch sizes, workers and read_timeout must be positive")
	}
	if c.Pipeline.PeriodBudgetMin <= 0 || c.Pipeline.PeriodBudgetMax <= 0 {
		return fmt.Errorf("pipeline period budgets must be positive")
	}
	if c.Pipeline.PeriodBudgetMin > c.Pipeline.PeriodBudgetMax {
		return fmt.Errorf("pipeline.period_budget_min must not exceed pipeline.period_budget_max")
	}
	if c.Recalc.ChunkPeriods <= 0 {
		return fmt.Errorf("recalc.chunk_periods must be positive")
	}
	return nil
}

func validTRPCTarget(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value != "" && !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://")
}

func splitURLs(raw string) []string {
	parts := strings.Split(raw, ",")
	urls := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			urls = append(urls, value)
		}
	}
	return urls
}
