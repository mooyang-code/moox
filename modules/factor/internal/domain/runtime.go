package domain

import "time"

// FactorPeriodState is one factor's outcome for the most recent live period.
type FactorPeriodState struct {
	FactorID       string
	Status         string
	FailedSubjects []string
	SpecHash       string
	DefinitionHash string
	// GapSubjects 是 FailedSubjects 中因回看窗口 K 线空洞而失败的标的（子集）。
	GapSubjects []string
}

// SetRunSummary is the latest live period of one factor set as reported by
// the compute engine.
type SetRunSummary struct {
	SetID          string
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

// LaneStatus is the backlog of one factor set's serial lane in the engine and
// how much of the set's universe has its lookback window loaded.
type LaneStatus struct {
	SetID            string
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
	Lanes           []LaneStatus
	RecentRuns      []SetRunSummary
	CatalogHash     string
	CatalogSyncedAt time.Time
	// Watermarks 是各集合的连续水位与阻塞状态。
	Watermarks []SetWatermark
}

// SetWatermark 是一个集合的连续水位：BlockedSince 非零表示阻塞在 BlockedPeriod（还没补到的周期）上。
type SetWatermark struct {
	SetID            string
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

// EngineSet is one enabled factor set as the engine computes it: the set, its
// enabled member definitions (with source) and whether its result dataset is
// ready to receive writes.
type EngineSet struct {
	Set         FactorSet
	Factors     []FactorDef
	ResultReady bool
}
