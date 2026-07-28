package domain

import (
	"encoding/json"
	"time"
)

const (
	FactorStatusEnabled  = "enabled"
	FactorStatusDisabled = "disabled"
)

// FactorDef is a locally managed factor definition.
type FactorDef struct {
	FactorID     string    `gorm:"column:c_factor_id;primaryKey"`
	Name         string    `gorm:"column:c_name"`
	SourceCode   string    `gorm:"column:c_source_code"`
	SourceHash   string    `gorm:"column:c_source_hash"`
	SourcePath   string    `gorm:"column:c_source_path"`
	InputColumns []string  `gorm:"column:c_input_columns_json;serializer:json"`
	Outputs      []string  `gorm:"column:c_outputs_json;serializer:json"`
	ParamsJSON   string    `gorm:"column:c_params_json"`
	LookbackRows int       `gorm:"column:c_lookback_rows"`
	Status       string    `gorm:"column:c_status"`
	CreateTime   time.Time `gorm:"column:c_ctime"`
	ModifyTime   time.Time `gorm:"column:c_mtime"`
}

// TableName returns the factor definition table.
func (FactorDef) TableName() string {
	return "t_factor_defs"
}

// BindingAllowsSubject reports whether a binding applies to one subject.
func BindingAllowsSubject(binding FactorBinding, subjectID string) bool {
	if binding.SubjectMode == "" || binding.SubjectMode == SubjectModeAll {
		return true
	}
	if binding.SubjectMode != SubjectModeInclude {
		return false
	}
	var subjects []string
	if err := json.Unmarshal([]byte(binding.SubjectsJSON), &subjects); err != nil {
		return false
	}
	for _, subject := range subjects {
		if subject == subjectID {
			return true
		}
	}
	return false
}
