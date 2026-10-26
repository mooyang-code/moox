package domain

import "time"

// FactorPeriodState is one member's outcome for the most recent live period.
type FactorPeriodState struct {
	MemberAlias    string
	FactorID       string
	Status         string
	FailedSubjects []string
	SpecHash       string
	DefinitionHash string
	// GapSubjects 是 FailedSubjects 中因回看窗口 K 线空洞而失败的标的（子集）。
	GapSubjects []string
	// ParamsSource、ParamsDigest 是这一期冻结参数的来源与摘要。
	ParamsSource string
	ParamsDigest string
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
	// ParamHealth 是各成员的参数状态：连续沿用的期数与最近一次离线冻结的时间，监控据此告警。
	ParamHealth []MemberParamHealth
	// OfflinePending 是本地还没补报给管理端的离线冻结数。
	OfflinePending int
}

// MemberParamHealth 是一个成员的参数状态。CarriedPeriods 是连续沿用（含离线沿用）参数的期数，新提交生效后归零，
// 从未提交过（默认值）不算沿用；AlertAfter 是超过多少期告警；LastOfflineAt 是最近一次离线冻结的时间（零值表示没有）。
type MemberParamHealth struct {
	TaskID         string
	Member         string
	CarriedPeriods int
	AlertAfter     int
	LastOfflineAt  time.Time
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
