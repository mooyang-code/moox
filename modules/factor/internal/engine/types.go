package engine

import "time"

// DataFrame is a small ordered in-memory table.
type DataFrame struct {
	Columns   []string
	Rows      [][]any
	DataTimes []time.Time
}

// FactorTask is the self-contained scheduler-to-engine task shape.
type FactorTask struct {
	TaskID        string
	SpaceID       string
	SourceDataset string
	TargetDataset string
	SubjectID     string
	Freq          string
	StartTime     time.Time
	EndTime       time.Time
	LookbackRows  int
	Factors       []FactorSpec
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

// FactorResult contains values aligned with the task's target rows.
type FactorResult struct {
	Columns map[string][]any
}
