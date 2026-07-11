package engine

import (
	"context"
	"time"
)

// Executor executes one factor task against an input frame.
type Executor interface {
	Execute(ctx context.Context, task *FactorTask, frame *DataFrame) (*FactorResult, error)
	Close() error
}

// DataFrame is a small ordered in-memory table.
type DataFrame struct {
	Columns   []string
	Rows      [][]any
	DataTimes []time.Time
}

// FactorTask is the self-contained scheduler-to-engine task shape.
type FactorTask struct {
	TaskID        string
	Kind          string
	SpaceID       string
	SourceDataset string
	TargetDataset string
	SubjectID     string
	Freq          string
	BarTime       time.Time
	LookbackBars  int
	Factors       []FactorSpec
}

// FactorSpec describes one Python factor module invocation.
type FactorSpec struct {
	FactorID      string
	Name          string
	SourceHash    string
	SourcePath    string
	Params        []int
	WritebackBars int
	ExtraColumns  []string
}

// FactorResult contains all returned result columns for a task.
type FactorResult struct {
	Columns     map[string]FactorColumnResult
	PerFactorMS map[string]int64
	ElapsedMS   int64
}

// FactorColumnResult contains one result column's tail values.
type FactorColumnResult struct {
	Tail   int
	Values []any
}
