package domain

import (
	"encoding/json"
	"time"
)

type RunnerStatus string

const (
	RunnerStatusDisabled RunnerStatus = "DISABLED"
	RunnerStatusEnabled  RunnerStatus = "ENABLED"
)

type Action string

const (
	ActionHold      Action = "hold"
	ActionRebalance Action = "rebalance"
)

type Strategy struct {
	ID           string
	Name         string
	ManifestYAML string
	SourceCode   string
	SourceHash   string
	CreatedAt    time.Time
}

func (Strategy) TableName() string { return "t_strategies" }

type StrategyRunner struct {
	ID                 string
	StrategyID         string
	SpaceID            string
	ViewID             string
	Frequency          string
	ParamsJSON         json.RawMessage
	LogicalAccountID   *string
	Status             RunnerStatus
	CurrentTargetsJSON json.RawMessage
	CommandSequence    int64
	LastResultID       *string
	LastSuccessAt      *time.Time
	LastError          *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (StrategyRunner) TableName() string { return "t_strategy_runners" }

type StrategyResult struct {
	ID              string
	RunnerID        string
	StrategyID      string
	TriggerBarTime  time.Time
	Namespace       string
	InputHash       string
	Action          Action
	OutputJSON      json.RawMessage
	CommandSequence *int64
	CreatedAt       time.Time
}

func (StrategyResult) TableName() string { return "t_strategy_results" }
