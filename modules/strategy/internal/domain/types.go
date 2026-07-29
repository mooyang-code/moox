package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

type InstrumentTarget struct {
	InstrumentID string `json:"instrument_id"`
	Quantity     string `json:"quantity"`
}

func (target *InstrumentTarget) UnmarshalJSON(data []byte) error {
	type instrumentTarget InstrumentTarget
	var decoded instrumentTarget
	if err := decodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*target = InstrumentTarget(decoded)
	return nil
}

type Output struct {
	Action    Action             `json:"action"`
	Targets   []InstrumentTarget `json:"targets,omitempty"`
	DebugInfo map[string]any     `json:"debug_info,omitempty"`
}

func (output *Output) UnmarshalJSON(data []byte) error {
	type strategyOutput Output
	var decoded strategyOutput
	if err := decodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*output = Output(decoded)
	return nil
}

type ExecutionRequest struct {
	RequestID      string
	StrategyID     string
	RunnerID       string
	TriggerBarTime string
	Namespace      string
	Params         any
	Data           any
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("multiple JSON values")
	}
	return nil
}
