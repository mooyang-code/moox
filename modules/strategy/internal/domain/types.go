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
	Kind         string
	ManifestYAML string
	CompiledJSON []byte
	SourceHash   string
	CreatedAt    time.Time
}

func (Strategy) TableName() string { return "t_strategies" }

type StrategyRunner struct {
	ID                 string
	StrategyID         string
	SpaceID            string
	SourceViewID       string
	Frequency          string
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
	PeriodTime      time.Time
	TargetsJSON     json.RawMessage
	DebugInfoJSON   json.RawMessage
	InputHash       string
	Action          Action
	CommandSequence *int64
	CreatedAt       time.Time
}

func (StrategyResult) TableName() string { return "t_strategy_results" }

type InstrumentTarget struct {
	InstrumentID string `json:"instrument_id"`
	TargetWeight string `json:"target_weight"`
}

// RuleState is the small amount of theoretical state that a stateful rule
// needs for its next evaluation. It deliberately contains no broker, order,
// fill, or actual-position data.
type RuleState struct {
	Signals []SignalState       `json:"signals,omitempty"`
	Batches []HoldingBatchState `json:"batches,omitempty"`
}

type SignalState struct {
	InstrumentID string `json:"instrument_id"`
	EnteredAt    int64  `json:"entered_at,omitempty"`
}

type HoldingBatchState struct {
	Offset        int               `json:"offset"`
	EstablishedAt int64             `json:"established_at"`
	ExpiresAt     int64             `json:"expires_at"`
	BaseWeights   map[string]string `json:"base_weights"`
}

func (s RuleState) Empty() bool { return len(s.Signals) == 0 && len(s.Batches) == 0 }

// Evaluation is the result of one pure evaluator invocation.
type Evaluation struct {
	Action     Action
	Targets    []InstrumentTarget
	DebugInfo  map[string]any
	RuleStates map[string]RuleState
}
