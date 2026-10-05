package domain

import "time"

const (
	FactorTypeTimeSeries   = "timeseries"
	FactorTypeCrossSection = "cross_section"
	MemberStatusEnabled    = "enabled"
	MemberStatusDisabled   = "disabled"

	SubjectModeAll     = "all"
	SubjectModeInclude = "include"

	SetStatusPending  = "pending"
	SetStatusEnabled  = "enabled"
	SetStatusDisabled = "disabled"
	SetStatusDeleting = "deleting"
)

// FactorSet groups definitions over one source dataset and frequency.
type FactorSet struct {
	SetID           string    `json:"set_id" gorm:"column:c_set_id;primaryKey"`
	SpaceID         string    `json:"space_id" gorm:"column:c_space_id"`
	SourceDatasetID string    `json:"source_dataset_id" gorm:"column:c_source_dataset_id"`
	Freq            string    `json:"freq" gorm:"column:c_freq"`
	SubjectMode     string    `json:"subject_mode" gorm:"column:c_subject_mode"`
	Subjects        []string  `json:"subjects" gorm:"column:c_subjects_json;serializer:json"`
	ResultDatasetID string    `json:"result_dataset_id" gorm:"column:c_result_dataset_id"`
	Status          string    `json:"status" gorm:"column:c_status"`
	CreatedAt       time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt       time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorSet) TableName() string { return "t_factor_sets" }

// FactorDef describes an algorithm only; it has no set binding and no run state.
type FactorDef struct {
	FactorID             string    `json:"factor_id" gorm:"column:c_factor_id;primaryKey"`
	Name                 string    `json:"name" gorm:"column:c_name"`
	FactorType           string    `json:"factor_type" gorm:"column:c_factor_type"`
	SourceCode           string    `json:"source_code" gorm:"column:c_source_code"`
	SourceHash           string    `json:"source_hash" gorm:"column:c_source_hash"`
	InputColumns         []string  `json:"input_columns" gorm:"column:c_input_columns_json;serializer:json"`
	Outputs              []string  `json:"outputs" gorm:"column:c_outputs_json;serializer:json"`
	ParamsJSON           string    `json:"params_json" gorm:"column:c_params_json"`
	LookbackPeriods      int       `json:"lookback_periods" gorm:"column:c_lookback_periods"`
	AllowPartialUniverse bool      `json:"allow_partial_universe" gorm:"column:c_allow_partial_universe"`
	CreatedAt            time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt            time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorDef) TableName() string { return "t_factor_defs" }

// FactorSetMember is a definition's runtime instance inside one factor set.
type FactorSetMember struct {
	SetID     string    `json:"set_id" gorm:"column:c_set_id;primaryKey"`
	FactorID  string    `json:"factor_id" gorm:"column:c_factor_id;primaryKey"`
	Status    string    `json:"status" gorm:"column:c_status"`
	CreatedAt time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

func (FactorSetMember) TableName() string { return "t_factor_set_members" }

// SetMember is the set-side view: a member together with its definition.
type SetMember struct {
	FactorSetMember
	Factor FactorDef
}

// FactorUsage reports one set that references a definition.
type FactorUsage struct {
	SetID  string
	Status string
}

// FactorInfo is the definition-side view: a definition and where it is used.
type FactorInfo struct {
	Factor FactorDef
	Usages []FactorUsage
}
