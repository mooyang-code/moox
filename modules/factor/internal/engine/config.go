// Package engine wires moox-factor-engine: it consumes collector period
// events, runs the factor expression pipeline and recalc jobs, and talks to
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
	Compute       ComputeConfig                    `yaml:"compute"`
	SeriesStore   SeriesStoreConfig                `yaml:"seriesstore"`
	Window        WindowConfig                     `yaml:"window"`
	Memory        MemoryConfig                     `yaml:"memory"`
	Sync          SyncConfig                       `yaml:"sync"`
	Pipeline      PipelineConfig                   `yaml:"pipeline"`
	Recalc        RecalcConfig                     `yaml:"recalc"`
	Gap           GapConfig                        `yaml:"gap"`
	Watermark     WatermarkConfig                  `yaml:"watermark"`
	Lifecycle     LifecycleConfig                  `yaml:"lifecycle"`
	Params        ParamsConfig                     `yaml:"params"`
}

// WatermarkConfig 是连续水位：每个集合从水位下一期逐期推进，前面有还没补到的周期就停住（docs/计划/因子连续水位详细设计.md）。
type WatermarkConfig struct {
	// StateFile 保存各集合的水位与阻塞状态。
	StateFile string `yaml:"state_file"`
	// MaxMissingRatio 是缺失阈值（未确认缺失的失败标的 ÷ 期望标的），超过的周期阻塞水位；SetRatios 按集合 ID 覆盖。
	MaxMissingRatio float64            `yaml:"max_missing_ratio"`
	SetRatios       map[string]float64 `yaml:"set_ratios"`
	// PollInterval 是阻塞或落后时按读回 Storage 继续推进的间隔（保底）；MaxPeriods 是一次推进最多处理的周期数。
	PollInterval time.Duration `yaml:"poll_interval"`
	MaxPeriods   int           `yaml:"max_periods"`
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

// ParamsConfig 是参数冻结（因子动态调参执行计划 2.4）：开算前向 FactorMgr 冻结参数，FreezeTimeout 内都失败时
// 在本地按沿用规则冻结（离线冻结记在 StateFile，恢复后补报）。
type ParamsConfig struct {
	StateFile     string        `yaml:"state_file"`
	FreezeTimeout time.Duration `yaml:"freeze_timeout"`
}

// LifecycleConfig 是生命周期控制器的配置：阶段持久化文件与检查周期。
type LifecycleConfig struct {
	StateFile string        `yaml:"state_file"`
	Interval  time.Duration `yaml:"interval"`
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

// ComputeConfig 是表达式执行器的资源配置（设计 7.2、7.5）。
type ComputeConfig struct {
	// Workers 是一次程序执行的并行度，默认 min(4, 核数)。
	Workers int `yaml:"workers"`
	// MaxMemory 是一个计算分块（实时周期视为 1 个周期的分块）估算峰值内存的上限，例如 "1GiB"、"512MiB"，默认 4GiB（默认因子 EMA 回看 5 万根，500 多个标的一期约需 1.1GB）。
	MaxMemory ByteSize `yaml:"max_memory"`
}

// SeriesStoreConfig 是本地时序存储（Pebble，数据永不删除，设计第 3 节）。
type SeriesStoreConfig struct {
	// Dir 是存储目录，相对路径按引擎根目录解析。
	Dir string `yaml:"dir"`
	// BlockCache 是 Pebble 块缓存大小，默认 512MiB，计入进程内存预算。
	BlockCache ByteSize `yaml:"block_cache"`
}

// WindowConfig 是内存列式窗口（设计第 4 节）。
type WindowConfig struct {
	// MaxMemory 是所有数据源窗口常驻内存的上限；新启用的集合会超出时拒绝启用。0 表示不限制。
	MaxMemory ByteSize `yaml:"max_memory"`
	// Headroom 是每个序列数组余量的根数，0 取窗口长度的 1/4（至少 16）。
	Headroom int `yaml:"headroom"`
	// Lookback 是回看数量 N 的全局默认（设计 8.1）：窗口长度、本地要保有的根数、集合内因子回看的上限。默认 1 万。
	Lookback int `yaml:"lookback"`
	// LookbackOverrides 按数据源覆盖 N，键是 "space/dataset/freq"，例如 "crypto/spot_kline_1h/1h": 8000。
	LookbackOverrides map[string]int `yaml:"lookback_overrides"`
}

// MaxLookbackLimit 是回看数量 N 的绝对上限，与表达式引擎的回看上限一致。
const MaxLookbackLimit = 100000

// DefaultWindowBars 是回看数量 N 的默认值：要装得下默认因子 EMA 的最大回看 49995 根。
const DefaultWindowBars = 50000

// LookbackFor 返回数据源的回看数量 N：先看按数据源的覆盖，再取全局默认。
func (c WindowConfig) LookbackFor(spaceID, datasetID, freq string) int {
	if n, ok := c.LookbackOverrides[spaceID+"/"+datasetID+"/"+freq]; ok && n > 0 {
		return n
	}
	if c.Lookback > 0 {
		return c.Lookback
	}
	return DefaultWindowBars
}

// MemoryConfig 是进程级内存预算（设计 4.4）：常驻窗口与块缓存先扣除，计算、回放、重算向准入器预留。
type MemoryConfig struct {
	// TotalBudget 是进程内存预算，初始化时按「总内存 × 60%」写入；0 表示不限制。
	TotalBudget ByteSize `yaml:"total_budget"`
}

// SyncConfig 是本地存储与 Storage 的同步。
type SyncConfig struct {
	// Workers 与 BatchSubjects 是同步时并行读取 Storage 的批数与每批标的数（Storage 单次最多 100 个）。
	Workers       int `yaml:"workers"`
	BatchSubjects int `yaml:"batch_subjects"`
	// FollowInterval 是读取 Storage 行变更日志、应用修正的间隔。
	FollowInterval time.Duration `yaml:"follow_interval"`
}

// ByteSize 是字节数，YAML 里可以写整数或带 KiB、MiB、GiB 后缀的字符串。
type ByteSize int64

func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	text := strings.TrimSpace(node.Value)
	units := []struct {
		suffix string
		scale  int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}
	scale := int64(1)
	for _, unit := range units {
		if strings.HasSuffix(text, unit.suffix) {
			text, scale = strings.TrimSpace(strings.TrimSuffix(text, unit.suffix)), unit.scale
			break
		}
	}
	var value int64
	if _, err := fmt.Sscan(text, &value); err != nil || value < 0 || fmt.Sprint(value) != text {
		return fmt.Errorf("无法解析的字节数 %q（示例：1GiB、512MiB、1073741824）", node.Value)
	}
	*b = ByteSize(value * scale)
	return nil
}

type PipelineConfig struct {
	WriteBatchRows  int           `yaml:"write_batch_rows"`
	PeriodBudgetMin time.Duration `yaml:"period_budget_min"`
	PeriodBudgetMax time.Duration `yaml:"period_budget_max"`
}

type RecalcConfig struct {
	ChunkPeriods int           `yaml:"chunk_periods"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

// GapConfig 是跳过账本与恢复：实时周期里标的本期数据取不到（上游失败、同步失败）、或内存窗口还没载入完成而跳过的结果，
// 账本永久记录，定时只填充重算。本地数据写入时保证连续，窗口里不会再有空洞。
type GapConfig struct {
	// LedgerDir 是账本目录（按月一个 JSONL 文件，永不自动删除，纳入备份）。
	LedgerDir string `yaml:"ledger_dir"`
	// SweepInterval 是恢复轮询间隔；Settle 是发现后至少等多久才尝试恢复（给自动补采留时间）。
	SweepInterval time.Duration `yaml:"sweep_interval"`
	Settle        time.Duration `yaml:"settle"`
	// GiveUpAfter 是超过多久仍补不上就记 gave_up 并告警，交给人工。
	GiveUpAfter time.Duration `yaml:"give_up_after"`
	// MaxGroupsPerSweep 与 MaxSpan 限制一轮恢复的工作量：一轮最多运行 MaxGroupsPerSweep 批（恢复区间相同的时序分组合并成一批），
	// 每个组一次最多处理 MaxSpan 个周期，避免长时间占住集合锁。
	MaxGroupsPerSweep int `yaml:"max_groups_per_sweep"`
	MaxSpan           int `yaml:"max_span"`
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
		Compute:       ComputeConfig{Workers: min(4, runtime.NumCPU()), MaxMemory: 4 << 30},
		SeriesStore:   SeriesStoreConfig{Dir: "./data/seriesstore", BlockCache: 512 << 20},
		Sync:          SyncConfig{Workers: 4, BatchSubjects: 100, FollowInterval: 30 * time.Second},
		Pipeline:      PipelineConfig{WriteBatchRows: 1000, PeriodBudgetMin: time.Minute, PeriodBudgetMax: 15 * time.Minute},
		Recalc:        RecalcConfig{ChunkPeriods: 500, PollInterval: 5 * time.Second},
		Gap: GapConfig{
			LedgerDir: "./data/gap", SweepInterval: 5 * time.Minute, Settle: 5 * time.Minute, GiveUpAfter: 6 * time.Hour,
			MaxGroupsPerSweep: 20, MaxSpan: 60,
		},
		Watermark: WatermarkConfig{StateFile: "./data/engine/watermarks.json", MaxMissingRatio: 0.1, PollInterval: 10 * time.Second, MaxPeriods: 30},
		Lifecycle: LifecycleConfig{StateFile: "./data/engine/lifecycle.json", Interval: 30 * time.Second},
		Params:    ParamsConfig{StateFile: "./data/engine/params.json", FreezeTimeout: 5 * time.Second},
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
	setInt(&c.Compute.Workers, d.Compute.Workers)
	if c.Compute.MaxMemory == 0 {
		c.Compute.MaxMemory = d.Compute.MaxMemory
	}
	setString(&c.SeriesStore.Dir, d.SeriesStore.Dir)
	if c.SeriesStore.BlockCache == 0 {
		c.SeriesStore.BlockCache = d.SeriesStore.BlockCache
	}
	setInt(&c.Window.Lookback, DefaultWindowBars)
	setInt(&c.Sync.Workers, d.Sync.Workers)
	setInt(&c.Sync.BatchSubjects, d.Sync.BatchSubjects)
	setDuration(&c.Sync.FollowInterval, d.Sync.FollowInterval)
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
	setString(&c.Watermark.StateFile, d.Watermark.StateFile)
	if c.Watermark.MaxMissingRatio == 0 {
		c.Watermark.MaxMissingRatio = d.Watermark.MaxMissingRatio
	}
	setDuration(&c.Watermark.PollInterval, d.Watermark.PollInterval)
	setInt(&c.Watermark.MaxPeriods, d.Watermark.MaxPeriods)
	setString(&c.Lifecycle.StateFile, d.Lifecycle.StateFile)
	setDuration(&c.Lifecycle.Interval, d.Lifecycle.Interval)
	setString(&c.Params.StateFile, d.Params.StateFile)
	setDuration(&c.Params.FreezeTimeout, d.Params.FreezeTimeout)
}

func (c *Config) resolvePaths(root string) {
	for _, path := range []*string{
		&c.CatalogSync.StateFile, &c.Storage.AuthSecretFile, &c.EventBus.CredentialFile,
		&c.Gap.LedgerDir, &c.Watermark.StateFile, &c.SeriesStore.Dir, &c.Lifecycle.StateFile, &c.Params.StateFile,
	} {
		value := strings.TrimSpace(*path)
		if value == "" || filepath.IsAbs(value) || strings.HasPrefix(value, "~") {
			continue
		}
		*path = filepath.Clean(filepath.Join(root, value))
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
	require(c.Compute.Workers > 0 && c.Compute.MaxMemory > 0, "compute.workers and compute.max_memory must be positive")
	require(c.Pipeline.PeriodBudgetMin <= c.Pipeline.PeriodBudgetMax, "pipeline.period_budget_min must not exceed period_budget_max")
	require(c.Recalc.ChunkPeriods > 0, "recalc.chunk_periods must be positive")
	require(c.Gap.SweepInterval > 0 && c.Gap.Settle >= 0 && c.Gap.GiveUpAfter > 0, "gap.sweep_interval, gap.settle and gap.give_up_after must be positive")
	require(c.Gap.MaxGroupsPerSweep > 0 && c.Gap.MaxSpan > 0, "gap.max_groups_per_sweep and gap.max_span must be positive")
	require(strings.TrimSpace(c.SeriesStore.Dir) != "" && c.SeriesStore.BlockCache > 0, "seriesstore.dir and seriesstore.block_cache are required")
	require(c.Sync.Workers > 0 && c.Sync.BatchSubjects > 0 && c.Sync.BatchSubjects <= 100 && c.Sync.FollowInterval > 0,
		"sync.workers must be positive, sync.batch_subjects must be within [1, 100] and sync.follow_interval must be positive")
	require(c.Window.MaxMemory >= 0 && c.Window.Headroom >= 0 && c.Memory.TotalBudget >= 0, "window.max_memory, window.headroom and memory.total_budget must not be negative")
	require(c.Window.Lookback >= 1 && c.Window.Lookback <= MaxLookbackLimit, fmt.Sprintf("window.lookback must be within [1, %d]", MaxLookbackLimit))
	for key, n := range c.Window.LookbackOverrides {
		require(strings.Count(key, "/") == 2 && n >= 1 && n <= MaxLookbackLimit,
			fmt.Sprintf("window.lookback_overrides[%q] must be keyed space/dataset/freq and within [1, %d]", key, MaxLookbackLimit))
	}
	require(c.Watermark.MaxMissingRatio > 0 && c.Watermark.MaxMissingRatio < 1 && c.Watermark.PollInterval > 0 && c.Watermark.MaxPeriods > 0,
		"watermark.max_missing_ratio must be within (0, 1), poll_interval and max_periods must be positive")
	for setID, ratio := range c.Watermark.SetRatios {
		require(ratio > 0 && ratio < 1, "watermark.set_ratios["+setID+"] must be within (0, 1)")
	}
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
