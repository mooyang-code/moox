// Package engine wires moox-factor-engine: it consumes collector period
// events, runs the Python factor pipeline and recalc jobs, and talks to
// moox-factor-mgr only through outbound FactorEngine calls.
package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// factorEngineCaller 是因子引擎的外部调用方身份。
const factorEngineCaller = "factor-engine"

type Config struct {
	Engine IdentityConfig `yaml:"engine"`
	// GatewayClient 是因子引擎访问 MooX 的 gatewayclient：外部方式，经外部接入调用 moox-factor-mgr 的
	// FactorEngine 服务和 Storage。
	GatewayClient gatewayclient.Config `yaml:"gateway_client"`
	Manager       ManagerConfig        `yaml:"manager"`
	CatalogSync   CatalogSyncConfig    `yaml:"catalog_sync"`
	Storage       StorageConfig        `yaml:"storage"`
	EventBus      EventBusConfig       `yaml:"eventbus"`
	Python        PythonConfig         `yaml:"python"`
	Pipeline      PipelineConfig       `yaml:"pipeline"`
	Recalc        RecalcConfig         `yaml:"recalc"`
}

// IdentityConfig names the engine; engine_id must stay stable across restarts
// so the manager recognizes the lease holder.
type IdentityConfig struct {
	ID                string        `yaml:"id"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
}

// ManagerConfig 是调用 moox-factor-mgr 的 FactorEngine 服务的设置。
type ManagerConfig struct {
	Timeout time.Duration `yaml:"timeout"`
}

// CatalogSyncConfig aligns catalog pulls to UTC multiples of interval plus
// offset, so changes land before the next period's event arrives.
type CatalogSyncConfig struct {
	Interval  time.Duration `yaml:"interval"`
	Offset    time.Duration `yaml:"offset"`
	StateFile string        `yaml:"state_file"`
}

// StorageConfig 中 auth_secret_file 保存 Storage Primary 的密钥，用来签名因子引擎的 AppID；访问 Storage 走
// gateway_client。
type StorageConfig struct {
	AuthSecretFile string `yaml:"auth_secret_file"`
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
	ChunkPeriods int           `yaml:"chunk_periods"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

// Load reads the engine config; relative paths resolve against the directory
// that contains the config directory.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read factor engine config %s: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse factor engine config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_ENGINE_ID")); value != "" {
		cfg.Engine.ID = value
	}
	if absolute, err := filepath.Abs(path); err == nil {
		root := filepath.Dir(absolute)
		if filepath.Base(root) == "config" {
			root = filepath.Dir(root)
		}
		cfg.resolvePaths(root)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Default() *Config {
	hostname, _ := os.Hostname()
	if short, _, ok := strings.Cut(hostname, "."); ok {
		hostname = short
	}
	return &Config{
		Engine:        IdentityConfig{ID: "factor-engine@" + hostname, HeartbeatInterval: 10 * time.Second},
		GatewayClient: gatewayclient.Config{Mode: gatewayclient.ModeAccess, Caller: factorEngineCaller},
		Manager:       ManagerConfig{Timeout: 30 * time.Second},
		CatalogSync:   CatalogSyncConfig{Interval: time.Minute, Offset: 45 * time.Second, StateFile: "./data/engine/catalog.json"},
		EventBus:      EventBusConfig{FetchMaxWait: 10 * time.Second},
		Python: PythonConfig{
			Bin: "python3", WorkerPath: "./pyworker/worker.py", FactorsDir: "./data/engine/factors",
			Workers: 8, TaskTimeout: 30 * time.Second,
		},
		Pipeline: PipelineConfig{
			ReadBatchSubjects: 100, ReadWorkers: 4, ReadTimeout: 60 * time.Second,
			WriteBatchRows: 1000, PeriodBudgetMin: time.Minute, PeriodBudgetMax: 15 * time.Minute,
		},
		Recalc: RecalcConfig{ChunkPeriods: 500, PollInterval: 5 * time.Second},
	}
}

func (c *Config) applyDefaults() {
	d := Default()
	setString(&c.Engine.ID, d.Engine.ID)
	setDuration(&c.Engine.HeartbeatInterval, d.Engine.HeartbeatInterval)
	setDuration(&c.Manager.Timeout, d.Manager.Timeout)
	setDuration(&c.CatalogSync.Interval, d.CatalogSync.Interval)
	setString(&c.CatalogSync.StateFile, d.CatalogSync.StateFile)
	setDuration(&c.EventBus.FetchMaxWait, d.EventBus.FetchMaxWait)
	setString(&c.Python.Bin, d.Python.Bin)
	setString(&c.Python.WorkerPath, d.Python.WorkerPath)
	setString(&c.Python.FactorsDir, d.Python.FactorsDir)
	setInt(&c.Python.Workers, d.Python.Workers)
	setDuration(&c.Python.TaskTimeout, d.Python.TaskTimeout)
	setInt(&c.Pipeline.ReadBatchSubjects, d.Pipeline.ReadBatchSubjects)
	setInt(&c.Pipeline.ReadWorkers, d.Pipeline.ReadWorkers)
	setDuration(&c.Pipeline.ReadTimeout, d.Pipeline.ReadTimeout)
	setInt(&c.Pipeline.WriteBatchRows, d.Pipeline.WriteBatchRows)
	setDuration(&c.Pipeline.PeriodBudgetMin, d.Pipeline.PeriodBudgetMin)
	setDuration(&c.Pipeline.PeriodBudgetMax, d.Pipeline.PeriodBudgetMax)
	setInt(&c.Recalc.ChunkPeriods, d.Recalc.ChunkPeriods)
	setDuration(&c.Recalc.PollInterval, d.Recalc.PollInterval)
}

func (c *Config) resolvePaths(root string) {
	for _, path := range []*string{
		&c.GatewayClient.KeyFile, &c.CatalogSync.StateFile, &c.Storage.AuthSecretFile, &c.EventBus.CredentialFile,
		&c.Python.WorkerPath, &c.Python.FactorsDir,
	} {
		value := strings.TrimSpace(*path)
		if value == "" || filepath.IsAbs(value) || strings.HasPrefix(value, "~") {
			continue
		}
		*path = filepath.Clean(filepath.Join(root, value))
	}
	if bin := strings.TrimSpace(c.Python.Bin); strings.Contains(bin, string(filepath.Separator)) && !filepath.IsAbs(bin) {
		c.Python.Bin = filepath.Clean(filepath.Join(root, bin))
	}
}

func (c *Config) Validate() error {
	var problems []string
	require := func(ok bool, problem string) {
		if !ok {
			problems = append(problems, problem)
		}
	}
	require(strings.TrimSpace(c.Engine.ID) != "", "engine.id is required")
	require(c.Engine.HeartbeatInterval > 0, "engine.heartbeat_interval must be positive")
	if err := c.GatewayClient.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	require(c.GatewayClient.Mode == gatewayclient.ModeAccess, "gateway_client.mode must be access")
	require(strings.TrimSpace(c.GatewayClient.Caller) == factorEngineCaller, "gateway_client.caller must be "+factorEngineCaller)
	require(strings.TrimSpace(c.GatewayClient.KeyFile) != "", "gateway_client.key_file is required")
	require(c.Manager.Timeout > 0, "manager.timeout must be positive")
	require(c.CatalogSync.Interval >= 10*time.Second, "catalog_sync.interval must be at least 10s")
	require(c.CatalogSync.Offset >= 0 && c.CatalogSync.Offset < c.CatalogSync.Interval, "catalog_sync.offset must be within [0, interval)")
	require(len(c.EventBus.URLs) > 0, "eventbus.urls must not be empty")
	require(c.Python.Workers > 0 && c.Python.TaskTimeout > 0, "python.workers and python.task_timeout must be positive")
	require(c.Pipeline.PeriodBudgetMin <= c.Pipeline.PeriodBudgetMax, "pipeline.period_budget_min must not exceed period_budget_max")
	require(c.Recalc.ChunkPeriods > 0, "recalc.chunk_periods must be positive")
	if len(problems) > 0 {
		return errors.New("invalid factor engine config: " + strings.Join(problems, "; "))
	}
	return nil
}

func setString(target *string, fallback string) {
	if strings.TrimSpace(*target) == "" {
		*target = fallback
	}
}

func setDuration(target *time.Duration, fallback time.Duration) {
	if *target == 0 {
		*target = fallback
	}
}

func setInt(target *int, fallback int) {
	if *target == 0 {
		*target = fallback
	}
}
