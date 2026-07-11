package domain

import "time"

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

type TargetWeight struct {
	InstrumentID string `json:"instrument_id"`
	TargetWeight string `json:"target_weight"`
}
type Output struct {
	Action    string         `json:"action"`
	Targets   []TargetWeight `json:"targets"`
	NextState map[string]any `json:"next_state"`
	DebugInfo map[string]any `json:"debug_info,omitempty"`
}
type Task struct {
	RunID, BindingID, StrategyID, Version, Namespace, SourceHash, TriggerBarTime, DataRevision, InputHash string
	PreviousState                                                                                         State
	PreviousTargets                                                                                       []TargetWeight
	Params                                                                                                map[string]any
	Data                                                                                                  []map[string]any
}
