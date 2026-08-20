package engine

import "time"

// DataFrame is a small ordered in-memory table.
type DataFrame struct {
	Columns    []string
	Rows       [][]any
	DataTimes  []time.Time
	SeriesTags []string
}

// FactorTask is the self-contained scheduler-to-engine task shape.
type FactorTask struct {
	TaskID          string
	BindingID       string
	SpaceID         string
	SourceViewID    string
	ResultDatasetID string
	SourceDataset   string
	TargetDataset   string
	SubjectID       string
	Freq            string
	PeriodTime      int64
	TriggerEventID  string
	TriggeredAt     time.Time
	StartTime       time.Time
	EndTime         time.Time
	LookbackPeriods int
	Factor          FactorSpec
}

// FactorSpec describes one Python factor module invocation.
type FactorSpec struct {
	FactorID     string
	Name         string
	SourceHash   string
	SourcePath   string
	InputColumns []string
	Outputs      []string
	ParamsJSON   string
}

// FactorResultRow is one output row with its complete time-series identity.
type FactorResultRow struct {
	DataTime  time.Time
	SeriesTag string
	Values    map[string]any
}

// FactorResult contains ordered output rows returned by one factor.
type FactorResult struct {
	Rows []FactorResultRow
}

// BatchTask groups period-scoped factor tasks that share one source frame.
// Each member keeps its original TaskID so writeback and manifests remain
// independent even though Python receives one request.
type BatchTask struct {
	BatchID string
	Tasks   []FactorTask
}

// BatchItemResult is the terminal result for one factor in a BatchTask.
type BatchItemResult struct {
	TaskID    string
	BindingID string
	Result    *FactorResult
	Err       error
}

// BatchResult contains per-factor results. A nil batch error means the
// protocol completed and individual failures are represented in Items.
type BatchResult struct {
	BatchID string
	Items   []BatchItemResult
}
