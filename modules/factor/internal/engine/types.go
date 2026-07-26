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
	LookbackBars  int
	Factors       []FactorSpec
}

// FactorSpec describes one Python factor module invocation.
type FactorSpec struct {
	FactorID   string
	Name       string
	SourceHash string
	SourcePath string
	Periods    []int
	Depends    []string
}

// FactorResult contains values aligned with the task's target rows.
type FactorResult struct {
	Columns map[string][]any
}
