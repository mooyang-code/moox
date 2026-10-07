package domain

import "time"

// FactorPeriodState is one factor's outcome for the most recent live period.
type FactorPeriodState struct {
	FactorID       string
	Status         string
	FailedSubjects []string
	SourceHash     string
}

// SetRunSummary is the latest live period of one factor set as reported by
// the compute engine.
type SetRunSummary struct {
	SetID          string
	LastPeriodTime int64
	LastStatus     string
	LagSeconds     int64
	Factors        []FactorPeriodState
	FailedSubjects []string
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
	PythonWorkers   int32
	PythonBusy      int32
	Lanes           []LaneStatus
	RecentRuns      []SetRunSummary
	CatalogHash     string
	CatalogSyncedAt time.Time
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
