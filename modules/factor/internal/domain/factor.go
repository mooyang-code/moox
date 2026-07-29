package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	FactorStatusEnabled  = "enabled"
	FactorStatusDisabled = "disabled"
)

// FactorDef is a locally managed factor definition.
type FactorDef struct {
	FactorID        string    `gorm:"column:c_factor_id;primaryKey"`
	Name            string    `gorm:"column:c_name"`
	SourceCode      string    `gorm:"column:c_source_code"`
	SourceHash      string    `gorm:"column:c_source_hash"`
	SourcePath      string    `gorm:"column:c_source_path"`
	InputColumns    []string  `gorm:"column:c_input_columns_json;serializer:json"`
	Outputs         []string  `gorm:"column:c_outputs_json;serializer:json"`
	ParamsJSON      string    `gorm:"column:c_params_json"`
	LookbackPeriods int       `gorm:"column:c_lookback_periods"`
	Status          string    `gorm:"column:c_status"`
	CreateTime      time.Time `gorm:"column:c_ctime"`
	ModifyTime      time.Time `gorm:"column:c_mtime"`
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
