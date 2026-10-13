package pipeline

import (
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

type Mode int

const (
	ModeLive Mode = iota + 1
	ModeRecalc
)

type Plan struct {
	Mode           Mode
	Set            domain.FactorSet
	Factors        []domain.FactorDef
	PeriodTime     time.Time // 实时模式的目标周期 T；补算为零值
	TargetStart    time.Time
	TargetEnd      time.Time
	Expected       []string
	Available      []string
	UpstreamFailed []string
	CarryColumns   []string
	WriteCarry     bool
	TriggerEventID string
	Budget         time.Duration
	// FillOnly 只填充：窗口检查通过才计算并写入，有洞、失败都不写任何字段（尤其不写失效 NULL），也不写 carry 列。
	// 用于空洞补齐后的恢复，见 docs/计划/因子窗口空洞防护详细设计.md。
	FillOnly bool
	// CommitSalt 非空时混入提交 ID，保证「期望重写」的写入不被相同内容的旧回执吞掉；补算与含空洞失效写入每次执行取新值。
	CommitSalt string
}

type Outcome struct {
	Status         string
	FailedSubjects []string
	Factors        []storageio.FactorState
	RowsWritten    int
	Warming        int
	StageDurations map[string]time.Duration
	// Items 是补算与填充的逐 (因子, 标的, 周期) 结果；实时周期不填。
	Items map[ItemKey]ItemStatus
}

type Config struct {
	ReadBatchSubjects int
	ReadWorkers       int
	ReadRetries       int
	// 后台预热的读取节流：批量更小、并发更低、每批之后停顿，预热时间可以长一些。
	WarmupBatchSubjects int
	WarmupWorkers       int
	WarmupBatchPause    time.Duration
	ReadTimeout         time.Duration
	WriteBatchRows      int
	WriteRetries        int
	WriteRetryBackoff   time.Duration
	PythonWorkers       int
	FactorsDir          string
	// BackgroundWarmup leaves live subjects without a loaded lookback window
	// out of their period and loads it in RunWarmup; without it such a subject
	// is read in full inside the period.
	BackgroundWarmup bool
	// GapCheck 开启回看窗口的 K 线空洞检查；关闭时行为与未引入该功能时完全一致。
	GapCheck bool
	// MassRatio 是整期大面积缺失的告警阈值：有空洞或上游失败的标的占预期的比例达到它时输出 ERROR 日志；0 表示不告警。
	MassRatio float64
	// GapSink 接收每次因空洞跳过的记录（账本）；nil 不记录。
	GapSink GapSink
}

type Runner struct {
	store   storageio.Store
	exec    pyexec.Executor
	clock   periodclock.Clock
	cfg     Config
	windows *liveWindowCache
	// firstBars 缓存标的的首根 K 线时间，用来区分新上市与空洞。
	firstBars firstBarCache
}

func NewRunner(store storageio.Store, exec pyexec.Executor, clock periodclock.Clock, cfg Config) *Runner {
	if cfg.ReadBatchSubjects <= 0 {
		cfg.ReadBatchSubjects = 100
	}
	if cfg.ReadWorkers <= 0 {
		cfg.ReadWorkers = 4
	}
	if cfg.WarmupBatchSubjects <= 0 {
		cfg.WarmupBatchSubjects = 10
	}
	if cfg.WarmupWorkers <= 0 {
		cfg.WarmupWorkers = 1
	}
	if cfg.WarmupBatchPause <= 0 {
		cfg.WarmupBatchPause = 500 * time.Millisecond
	}
	if cfg.ReadRetries <= 0 {
		cfg.ReadRetries = 2
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 60 * time.Second
	}
	if cfg.WriteBatchRows <= 0 {
		cfg.WriteBatchRows = 1000
	}
	if cfg.WriteRetries <= 0 {
		cfg.WriteRetries = 3
	}
	if cfg.PythonWorkers <= 0 {
		cfg.PythonWorkers = 1
	}
	return &Runner{store: store, exec: exec, clock: clock, cfg: cfg, windows: newLiveWindowCache(cfg.BackgroundWarmup)}
}

type LoadResult struct {
	// Gaps 是回看窗口的空洞检查结果，由 Run 在读取之后填入；没有空洞或未开启检查时为零值。
	Gaps           GapReport
	Frames         map[string]*storageio.Frame
	Available      []string
	FailedSubjects []string
	// WarmingSubjects are left out of the period until their lookback window
	// is loaded; they are also listed in FailedSubjects.
	WarmingSubjects []string
}

type Computation struct {
	// CrossPeriods 记录截面因子每个目标周期的结果：补算一次计算多个周期，块级的失败名单推不出各周期的结果。
	CrossPeriods map[string]map[int64]periodOutcome
	Results      map[string]map[string]pyexec.ItemResult
	FactorStates map[string]storageio.FactorState
	BudgetHit    bool
}

// periodOutcome 是截面因子一个目标周期的结果。
type periodOutcome string

const (
	periodOK     periodOutcome = "ok"
	periodGap    periodOutcome = "gap"
	periodFailed periodOutcome = "failed"
)

// ItemKey 标识补算或填充里的一个 (因子, 标的, 周期)。
type ItemKey struct {
	FactorID string
	Subject  string
	Period   time.Time
}

// ItemStatus 是一个 (因子, 标的, 周期) 的结果：written 已取得有效值并写入，gap 窗口有洞，failed 计算错误、无效值或没有源行。
type ItemStatus string

const (
	ItemWritten ItemStatus = "written"
	ItemGap     ItemStatus = "gap"
	ItemFailed  ItemStatus = "failed"
)
