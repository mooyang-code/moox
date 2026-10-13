// Package engine wires moox-factor-engine: it consumes collector period
// events, runs the Python factor pipeline and recalc jobs, and talks to
// moox-factor-mgr only through outbound FactorEngine calls.
package engine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

type Config struct {
	GatewayClient gatewayclient.ExternalFileConfig `yaml:"gateway_client"`
	sourcePath    string                           `yaml:"-"`
	Engine        IdentityConfig                   `yaml:"engine"`
	Manager       ManagerConfig                    `yaml:"manager"`
	CatalogSync   CatalogSyncConfig                `yaml:"catalog_sync"`
	Storage       StorageConfig                    `yaml:"storage"`
	EventBus      EventBusConfig                   `yaml:"eventbus"`
	Python        PythonConfig                     `yaml:"python"`
	Pipeline      PipelineConfig                   `yaml:"pipeline"`
	Recalc        RecalcConfig                     `yaml:"recalc"`
	Gap           GapConfig                        `yaml:"gap"`
}

// IdentityConfig names the engine; engine_id must stay stable across restarts
// so the manager recognizes the lease holder.
type IdentityConfig struct {
	ID                string        `yaml:"id"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
}

// ManagerConfig bounds manager calls made through the shared external client.
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

// StorageConfig supplies application authentication independently of the
// external gateway credential.
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
	// 预热读取节流（默认 10 个标的一批、1 个并发、批间停顿 500ms）。
	WarmupBatchSubjects int           `yaml:"warmup_batch_subjects"`
	WarmupWorkers       int           `yaml:"warmup_workers"`
	WarmupBatchPause    time.Duration `yaml:"warmup_batch_pause"`
	WriteBatchRows      int           `yaml:"write_batch_rows"`
	PeriodBudgetMin     time.Duration `yaml:"period_budget_min"`
	PeriodBudgetMax     time.Duration `yaml:"period_budget_max"`
}

type RecalcConfig struct {
	ChunkPeriods int           `yaml:"chunk_periods"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

// GapConfig 是 K 线空洞防护：回看窗口有洞的因子对该标的当期失败，账本永久记录，补齐后定时只填充重算。
// 默认关闭：下游（Storage、策略、Trade）全部支持 gap_subjects 与保持标记之后才打开。
// 设计见 docs/计划/因子窗口空洞防护详细设计.md。
type GapConfig struct {
	Enabled bool `yaml:"enabled"`
	// LedgerDir 是账本目录（按月一个 JSONL 文件，永不自动删除，纳入备份）。
	LedgerDir string `yaml:"ledger_dir"`
	// SweepInterval 是恢复轮询间隔；Settle 是发现后至少等多久才尝试恢复（给自动补采留时间）。
	SweepInterval time.Duration `yaml:"sweep_interval"`
	Settle        time.Duration `yaml:"settle"`
	// GiveUpAfter 是超过多久仍补不上就记 gave_up 并告警，交给人工。
	GiveUpAfter time.Duration `yaml:"give_up_after"`
	// MaxGroupsPerSweep 与 MaxSpan 限制一轮恢复的工作量：每个组一次最多处理 MaxSpan 个周期，避免长时间占住集合锁。
	MaxGroupsPerSweep int `yaml:"max_groups_per_sweep"`
	MaxSpan           int `yaml:"max_span"`
	// MassRatio 是整期大面积缺失的告警阈值：有空洞或上游失败的标的占预期的比例达到它时输出 ERROR 日志；0 表示不告警。
	MassRatio float64 `yaml:"mass_ratio"`
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
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("factor engine configuration requires one YAML document")
	}
	cfg.applyDefaults()
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_ENGINE_ID")); value != "" {
		cfg.Engine.ID = value
	}
	if absolute, err := filepath.Abs(path); err == nil {
		cfg.sourcePath = absolute
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
		GatewayClient: gatewayclient.ExternalFileConfig{Caller: "factor-engine", KeyFile: "../secrets/access-factor-engine.key"},
		Engine:        IdentityConfig{ID: "factor-engine@" + hostname, HeartbeatInterval: 10 * time.Second},
		Manager:       ManagerConfig{Timeout: 30 * time.Second},
		CatalogSync:   CatalogSyncConfig{Interval: time.Minute, Offset: 45 * time.Second, StateFile: "./data/engine/catalog.json"},
		Storage:       StorageConfig{AuthSecretFile: "./secrets/storage-primary-auth.secret"},
		EventBus:      EventBusConfig{FetchMaxWait: 10 * time.Second},
		Python: PythonConfig{
			Bin: "python3", WorkerPath: "./pyworker/worker.py", FactorsDir: "./data/engine/factors",
			Workers: autoPythonWorkers(runtime.NumCPU()), TaskTimeout: 30 * time.Second,
		},
		Pipeline: PipelineConfig{
			ReadBatchSubjects: 100, ReadWorkers: autoReadWorkers(runtime.NumCPU()), ReadTimeout: 60 * time.Second,
			WarmupBatchSubjects: 10, WarmupWorkers: 1, WarmupBatchPause: 500 * time.Millisecond,
			WriteBatchRows: 1000, PeriodBudgetMin: time.Minute, PeriodBudgetMax: 15 * time.Minute,
		},
		Recalc: RecalcConfig{ChunkPeriods: 500, PollInterval: 5 * time.Second},
		Gap: GapConfig{
			LedgerDir: "./data/gap", SweepInterval: 5 * time.Minute, Settle: 5 * time.Minute, GiveUpAfter: 6 * time.Hour,
			MaxGroupsPerSweep: 20, MaxSpan: 60, MassRatio: 0.2,
		},
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
	setInt(&c.Pipeline.WarmupBatchSubjects, d.Pipeline.WarmupBatchSubjects)
	setInt(&c.Pipeline.WarmupWorkers, d.Pipeline.WarmupWorkers)
	setDuration(&c.Pipeline.WarmupBatchPause, d.Pipeline.WarmupBatchPause)
	setInt(&c.Pipeline.WriteBatchRows, d.Pipeline.WriteBatchRows)
	setDuration(&c.Pipeline.PeriodBudgetMin, d.Pipeline.PeriodBudgetMin)
	setDuration(&c.Pipeline.PeriodBudgetMax, d.Pipeline.PeriodBudgetMax)
	setInt(&c.Recalc.ChunkPeriods, d.Recalc.ChunkPeriods)
	setDuration(&c.Recalc.PollInterval, d.Recalc.PollInterval)
	setString(&c.Gap.LedgerDir, d.Gap.LedgerDir)
	setDuration(&c.Gap.SweepInterval, d.Gap.SweepInterval)
	setDuration(&c.Gap.Settle, d.Gap.Settle)
	setDuration(&c.Gap.GiveUpAfter, d.Gap.GiveUpAfter)
	setInt(&c.Gap.MaxGroupsPerSweep, d.Gap.MaxGroupsPerSweep)
	setInt(&c.Gap.MaxSpan, d.Gap.MaxSpan)
	if c.Gap.MassRatio == 0 {
		c.Gap.MassRatio = d.Gap.MassRatio
	}
}

func (c *Config) resolvePaths(root string) {
	for _, path := range []*string{
		&c.CatalogSync.StateFile, &c.Storage.AuthSecretFile, &c.EventBus.CredentialFile,
		&c.Python.WorkerPath, &c.Python.FactorsDir, &c.Gap.LedgerDir,
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
	require(c.Manager.Timeout > 0, "manager.timeout must be positive")
	require(c.GatewayClient.Caller == "factor-engine", "gateway_client.caller must be factor-engine")
	if err := c.GatewayClient.Validate(); err != nil {
		problems = append(problems, err.Error())
	}
	require(c.CatalogSync.Interval >= 10*time.Second, "catalog_sync.interval must be at least 10s")
	require(c.CatalogSync.Offset >= 0 && c.CatalogSync.Offset < c.CatalogSync.Interval, "catalog_sync.offset must be within [0, interval)")
	require(strings.TrimSpace(c.Storage.AuthSecretFile) != "", "storage.auth_secret_file is required")
	require(len(c.EventBus.URLs) > 0, "eventbus.urls must not be empty")
	require(c.Python.Workers > 0 && c.Python.TaskTimeout > 0, "python.workers and python.task_timeout must be positive")
	require(c.Pipeline.PeriodBudgetMin <= c.Pipeline.PeriodBudgetMax, "pipeline.period_budget_min must not exceed period_budget_max")
	require(c.Recalc.ChunkPeriods > 0, "recalc.chunk_periods must be positive")
	require(c.Gap.SweepInterval > 0 && c.Gap.Settle >= 0 && c.Gap.GiveUpAfter > 0, "gap.sweep_interval, gap.settle and gap.give_up_after must be positive")
	require(c.Gap.MaxGroupsPerSweep > 0 && c.Gap.MaxSpan > 0, "gap.max_groups_per_sweep and gap.max_span must be positive")
	require(c.Gap.MassRatio >= 0 && c.Gap.MassRatio <= 1, "gap.mass_ratio must be within [0, 1]")
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

// autoPythonWorkers 按目标机器核数选择 Python 因子进程数（配置里 workers 不写或为 0 时）：
// 取核数的 3/4（计算只在每分钟周期里突发，其余时间空闲），留出余量给 Go 进程与同机其他服务，范围 [4, 64]。
func autoPythonWorkers(cpus int) int {
	return min(64, max(4, cpus*3/4))
}

// autoReadWorkers 按核数选择读取并发（read_workers 不写或为 0 时），范围 [4, 16]。
func autoReadWorkers(cpus int) int {
	return min(16, max(4, cpus/4))
}

func setInt(target *int, fallback int) {
	if *target == 0 {
		*target = fallback
	}
}
