package domain

import (
	"time"

	"github.com/mooyang-code/moox/packages/seriesset"
)

// FactorPeriodState is one member's outcome for the most recent live period.
type FactorPeriodState struct {
	MemberKey string
	// MemberKind 是成员类型：fixed 或 var。
	MemberKind     string
	FactorID       string
	Status         string
	FailedSubjects []string
	SpecHash       string
	DefinitionHash string
	// FailedSeries 是按序列（标的、series_tag）的失败名单，口径同 FailedSubjects。
	FailedSeries []seriesset.Series
	// SkippedSeriesTags 是被跳过的 series_tag（截面因子全集缺数）：这些序列的结果写 NULL。
	// 全部 tag 都跳过时 Status 为 skipped；只跳过一部分时 Status 为 degraded，并在这里列出。
	SkippedSeriesTags []string
}

// TaskRunSummary is the latest live period of one factor task as reported by
// the compute engine.
type TaskRunSummary struct {
	TaskID         string
	LastPeriodTime int64
	// CompletedAt 是引擎写完 LastPeriodTime 这一期的 Unix 秒；0 表示未知（引擎重启前的期次）。
	CompletedAt    int64
	LastStatus     string
	LagSeconds     int64
	Factors        []FactorPeriodState
	FailedSubjects []string
	// WarmingSubjects were left out of the period while their lookback
	// window loads; their results are not formal.
	WarmingSubjects int32
}

// RunnerStatus is the backlog of one factor task's serial runner in the engine and
// how much of the set's universe has its lookback window loaded.
type RunnerStatus struct {
	TaskID           string
	Queued           int32
	Active           bool
	WarmupState      string
	WarmSubjects     int32
	ExpectedSubjects int32
	// Paused 表示引擎模块被有意停下（暂停，以及放行前的恢复追赶），任务因此不计算。
	Paused bool
	// Checks 是引擎计算前对任务做的检查；有不通过项的任务不计算，不通过的项写明原因和处理办法。
	Checks []TaskCheck
}

// TaskCheck 是因子任务的一项计算前检查的结果。
type TaskCheck struct {
	Name   string
	Status string // TaskCheckPass | TaskCheckWarn | TaskCheckFail
	// Message 是没通过的原因；Remedy 是处理办法（例如要执行的命令）。
	Message string
	Remedy  string
}

// 检查结果：警告不挡计算，不通过的任务不计算。
const (
	TaskCheckPass = "PASS"
	TaskCheckWarn = "WARN"
	TaskCheckFail = "FAIL"
)

// TaskChecksPassed 报告检查里是否没有不通过项。
func TaskChecksPassed(checks []TaskCheck) bool {
	for _, check := range checks {
		if check.Status == TaskCheckFail {
			return false
		}
	}
	return true
}

// EngineIdentity names one running moox-factor-engine process.
type EngineIdentity struct {
	EngineID string
	BootID   string
	Version  string
}

// EngineStatus is the runtime state an engine reports with each heartbeat.
type EngineStatus struct {
	ConsumerRunning bool
	ComputeWorkers  int32
	// ExprVersion 是引擎内置的 expr 语义版本；与管理端不同时两边算出的 definition_hash 不一致。
	ExprVersion     string
	Runners         []RunnerStatus
	RecentRuns      []TaskRunSummary
	CatalogHash     string
	CatalogSyncedAt time.Time
	// Watermarks 是各任务的连续水位与阻塞状态。
	Watermarks []SetWatermark
	// Lifecycle 是引擎模块自身的生命周期状态（暂停是否已收敛、追赶前置条件）。
	Lifecycle EngineLifecycle
	// UDFModules 是引擎当前目录引用的各自定义算子模块（版本）的状态。
	UDFModules []UDFModuleStatus
}

// 自定义算子模块在引擎上的状态。
const (
	UDFModuleReady         = "ready"
	UDFModuleFetching      = "fetching"
	UDFModuleFetchFailed   = "fetch_failed"
	UDFModuleCompileFailed = "compile_failed"
)

// UDFModuleStatus 是引擎上报的一个自定义算子模块（版本）的状态。
type UDFModuleStatus struct {
	Hash  string
	Name  string
	State string
	Error string
}

// EngineLifecycle 是引擎模块级的生命周期状态。Quiesced 为真表示本次暂停已收敛（在途写结果操作都已结束）。
type EngineLifecycle struct {
	Stage          string
	PausedSince    time.Time
	QuiescedAt     time.Time
	Quiesced       bool
	CatchupBlocked string
}

// SetWatermark 是一个任务的连续水位：BlockedSince 非零表示阻塞在 BlockedPeriod（还没补到的周期）上。
type SetWatermark struct {
	TaskID           string
	CommittedThrough time.Time
	Target           time.Time
	BlockedSince     time.Time
	BlockedPeriod    time.Time
	BlockedReason    string
}

// EngineInfo is the manager's view of the engine holding the lease.
type EngineInfo struct {
	EngineIdentity
	Online          bool
	LastHeartbeatAt time.Time
	CatalogHash     string
	CatalogSyncedAt time.Time
	CatalogInSync   bool
}

// EngineTask is one enabled factor task as the engine computes it: the set, its
// enabled members (alias with definition and source) and whether its result
// dataset is ready to receive writes.
type EngineTask struct {
	Set         FactorTask
	Members     []MemberDef
	ResultReady bool
}
