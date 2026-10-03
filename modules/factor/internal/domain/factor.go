package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	FactorTypeTimeSeries   = "timeseries"
	FactorTypeCrossSection = "cross_section"
	FactorStatusEnabled    = "enabled"
	FactorStatusDisabled   = "disabled"
)

// FactorDef is a locally managed factor definition.
type FactorDef struct {
	FactorID             string    `json:"factor_id" gorm:"column:c_factor_id;primaryKey"`
	SetID                string    `json:"set_id" gorm:"column:c_set_id"`
	Name                 string    `json:"name" gorm:"column:c_name"`
	FactorType           string    `json:"factor_type" gorm:"column:c_factor_type"`
	SourceCode           string    `json:"source_code" gorm:"column:c_source_code"`
	SourceHash           string    `json:"source_hash" gorm:"column:c_source_hash"`
	InputColumns         []string  `json:"input_columns" gorm:"column:c_input_columns_json;serializer:json"`
	Outputs              []string  `json:"outputs" gorm:"column:c_outputs_json;serializer:json"`
	ParamsJSON           string    `json:"params_json" gorm:"column:c_params_json"`
	LookbackPeriods      int       `json:"lookback_periods" gorm:"column:c_lookback_periods"`
	AllowPartialUniverse bool      `json:"allow_partial_universe" gorm:"column:c_allow_partial_universe"`
	Status               string    `json:"status" gorm:"column:c_status"`
	CreatedAt            time.Time `json:"created_at" gorm:"column:c_ctime"`
	UpdatedAt            time.Time `json:"updated_at" gorm:"column:c_mtime"`
}

// TableName returns the factor definition table.
func (FactorDef) TableName() string {
	return "t_factor_defs"
}

// FactorAllowsDegraded reports whether a definition explicitly permits incomplete panels.
func FactorAllowsDegraded(factor FactorDef) bool {
	var params map[string]any
	if json.Unmarshal([]byte(factor.ParamsJSON), &params) != nil {
		return false
	}
	allowed, _ := params["allow_degraded"].(bool)
	return allowed
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

// NormalizeBindingSubjects validates and canonicalizes a binding subject scope.
func NormalizeBindingSubjects(mode, raw string) (string, error) {
	switch mode {
	case SubjectModeAll:
		return DefaultSubjectsJSON, nil
	case SubjectModeInclude:
		var subjects []string
		if err := json.Unmarshal([]byte(raw), &subjects); err != nil {
			return "", fmt.Errorf("subjects_json must be a JSON string array: %w", err)
		}
		unique := make(map[string]struct{}, len(subjects))
		for _, subject := range subjects {
			subject = strings.TrimSpace(subject)
			if subject == "" {
				return "", fmt.Errorf("subjects_json must not contain empty subjects")
			}
			unique[subject] = struct{}{}
		}
		if len(unique) == 0 {
			return "", fmt.Errorf("subjects_json must contain at least one subject in include mode")
		}
		normalized := make([]string, 0, len(unique))
		for subject := range unique {
			normalized = append(normalized, subject)
		}
		sort.Strings(normalized)
		encoded, err := json.Marshal(normalized)
		if err != nil {
			return "", fmt.Errorf("marshal normalized subjects_json: %w", err)
		}
		return string(encoded), nil
	default:
		return "", fmt.Errorf("subject_mode must be %q or %q", SubjectModeAll, SubjectModeInclude)
	}
}
