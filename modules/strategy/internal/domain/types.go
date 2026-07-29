package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	ActionHold      = "hold"
	ActionRebalance = "rebalance"
)

type StrategyDefinition struct {
	StrategyID         string    `gorm:"column:c_strategy_id;primaryKey"`
	Version            string    `gorm:"column:c_version;primaryKey"`
	API                string    `gorm:"column:c_api_version"`
	ManifestYAML       string    `gorm:"column:c_manifest_yaml"`
	SourceCode         string    `gorm:"column:c_source_code"`
	SourceHash         string    `gorm:"column:c_source_hash"`
	StateSchemaVersion int       `gorm:"column:c_state_schema_version"`
	Status             string    `gorm:"column:c_status"`
	CreateTime         time.Time `gorm:"column:c_ctime"`
	ModifyTime         time.Time `gorm:"column:c_mtime"`
}

func (StrategyDefinition) TableName() string { return "t_strategy_defs" }

type Binding struct {
	BindingID       string `gorm:"column:c_binding_id;primaryKey"`
	StrategyID      string `gorm:"column:c_strategy_id"`
	StrategyVersion string `gorm:"column:c_strategy_version"`
	SpaceID         string `gorm:"column:c_space_id"`
	ViewID          string `gorm:"column:c_view_id"`
	Freq            string `gorm:"column:c_freq"`
	ParamsJSON      string `gorm:"column:c_params_json"`
	GroupID         string `gorm:"column:c_group_id"`
	CapitalWeight   string `gorm:"column:c_capital_weight"`
	Status          string `gorm:"column:c_status"`
}

func (Binding) TableName() string { return "t_strategy_bindings" }

type State struct {
	BindingID       string `gorm:"column:c_binding_id;primaryKey"`
	StrategyVersion string `gorm:"column:c_strategy_version"`
	Revision        int64  `gorm:"column:c_state_revision"`
	StateJSON       string `gorm:"column:c_state_json"`
	LastRunID       string `gorm:"column:c_last_run_id"`
}

func (State) TableName() string { return "t_strategy_states" }

type StrategyRun struct {
	RunID                 string    `gorm:"column:c_run_id;primaryKey" json:"run_id"`
	BindingID             string    `gorm:"column:c_binding_id" json:"binding_id"`
	StrategyVersion       string    `gorm:"column:c_strategy_version" json:"strategy_version"`
	Namespace             string    `gorm:"column:c_namespace" json:"namespace"`
	TriggerBarTime        string    `gorm:"column:c_trigger_bar_time" json:"trigger_bar_time"`
	DataRevision          string    `gorm:"column:c_data_revision" json:"data_revision"`
	InputHash             string    `gorm:"column:c_input_hash" json:"input_hash"`
	PreviousStateRevision int64     `gorm:"column:c_previous_state_revision" json:"previous_state_revision"`
	Status                string    `gorm:"column:c_status" json:"status"`
	Action                string    `gorm:"column:c_action" json:"action"`
	OutputJSON            string    `gorm:"column:c_output_json" json:"output_json"`
	CreateTime            time.Time `gorm:"column:c_ctime" json:"create_time"`
}

func (StrategyRun) TableName() string { return "t_strategy_runs" }

type TargetPosition struct {
	InstrumentID   string `json:"instrument_id"`
	Symbol         string `json:"symbol"`
	TargetQuantity string `json:"target_quantity"`
	Reason         string `json:"reason,omitempty"`
	SourceTime     string `json:"source_time,omitempty"`
	DataRevision   string `json:"data_revision,omitempty"`
}

func (t *TargetPosition) UnmarshalJSON(data []byte) error {
	type targetPosition TargetPosition
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	allowed := map[string]struct{}{
		"instrument_id": {}, "symbol": {}, "target_quantity": {},
		"reason": {}, "source_time": {}, "data_revision": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown target field %q", field)
		}
	}
	var decoded targetPosition
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = TargetPosition(decoded)
	return nil
}

type Output struct {
	Action    string           `json:"action"`
	Targets   []TargetPosition `json:"targets"`
	NextState map[string]any   `json:"next_state"`
	DebugInfo map[string]any   `json:"debug_info,omitempty"`
}
type Task struct {
	RunID, BindingID, StrategyID, Version, SpaceID, Namespace, Freq, SourceHash, TriggerBarTime, DataRevision, InputHash string
	PreviousState                                                                                                        State
	PreviousTargets                                                                                                      []TargetPosition
	Params                                                                                                               map[string]any
	Data                                                                                                                 []map[string]any
}

type ExecutionBinding struct {
	ExecutionBindingID string `gorm:"column:c_execution_binding_id;primaryKey"`
	GroupID            string `gorm:"column:c_group_id"`
	ExchangeAccountID  string `gorm:"column:c_exchange_account_id"`
	Mode               string `gorm:"column:c_mode"`
	Status             string `gorm:"column:c_status"`
}

func (ExecutionBinding) TableName() string { return "t_strategy_execution_bindings" }
