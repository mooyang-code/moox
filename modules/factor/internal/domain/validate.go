package domain

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var ReservedColumns = []string{"subject_id", "freq", "data_time", "series_tag"}

var (
	factorIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	columnPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	reservedColumns = map[string]struct{}{
		"subject_id": {}, "freq": {}, "data_time": {}, "series_tag": {},
	}
)

// ValidateSet checks the stable identity and subject-scope contract of a set.
func ValidateSet(set FactorSet) error {
	for name, value := range map[string]string{
		"set_id": set.SetID, "space_id": set.SpaceID, "source_dataset_id": set.SourceDatasetID,
		"freq": set.Freq, "result_dataset_id": set.ResultDatasetID,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%s is required and must not have surrounding whitespace", name)
		}
	}
	if _, err := ParseFrequency(set.Freq); err != nil {
		return fmt.Errorf("invalid freq %q: %w", set.Freq, err)
	}
	if set.SetID != SetID(set.SourceDatasetID, set.Freq) {
		return fmt.Errorf("set_id must be %q", SetID(set.SourceDatasetID, set.Freq))
	}
	if set.ResultDatasetID != ResultDatasetID(set.SourceDatasetID, set.Freq) {
		return fmt.Errorf("result_dataset_id must be %q", ResultDatasetID(set.SourceDatasetID, set.Freq))
	}
	switch set.SubjectMode {
	case SubjectModeAll:
		if len(set.Subjects) != 0 {
			return fmt.Errorf("subjects must be empty when subject_mode is %q", SubjectModeAll)
		}
	case SubjectModeInclude:
		if len(set.Subjects) == 0 {
			return fmt.Errorf("subjects must contain at least one subject in include mode")
		}
		seen := make(map[string]struct{}, len(set.Subjects))
		for _, subject := range set.Subjects {
			if strings.TrimSpace(subject) == "" || subject != strings.TrimSpace(subject) {
				return fmt.Errorf("subjects must not contain empty or whitespace-padded values")
			}
			if _, ok := seen[subject]; ok {
				return fmt.Errorf("subjects contains duplicate subject %q", subject)
			}
			seen[subject] = struct{}{}
		}
	default:
		return fmt.Errorf("subject_mode must be %q or %q", SubjectModeAll, SubjectModeInclude)
	}
	switch set.Status {
	case SetStatusPending, SetStatusEnabled, SetStatusDisabled, SetStatusDeleting:
		return nil
	default:
		return fmt.Errorf("invalid factor set status %q", set.Status)
	}
}

// ValidateDefinition checks the static rules of a definition; it does not depend on any dataset.
func ValidateDefinition(def FactorDef) error {
	if !factorIDPattern.MatchString(def.FactorID) {
		return fmt.Errorf("factor_id must match [A-Za-z_][A-Za-z0-9_]*")
	}
	if strings.TrimSpace(def.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if err := ValidateFactorType(def.FactorType); err != nil {
		return err
	}
	if strings.TrimSpace(def.SourceCode) == "" {
		return fmt.Errorf("source_code is required")
	}
	if def.SourceHash != SourceHash(def.SourceCode) {
		return fmt.Errorf("source_hash does not match source_code")
	}
	if def.LookbackPeriods < 1 {
		return fmt.Errorf("lookback_periods must be at least 1")
	}
	if def.AllowPartialUniverse && def.FactorType != FactorTypeCrossSection {
		return fmt.Errorf("allow_partial_universe is only valid for %s factors", FactorTypeCrossSection)
	}
	if err := validateJSONObject(def.ParamsJSON); err != nil {
		return err
	}
	if len(def.InputColumns) == 0 {
		return fmt.Errorf("input_columns must contain at least one column")
	}
	if len(def.Outputs) == 0 {
		return fmt.Errorf("outputs must contain at least one column")
	}
	seenInputs := make(map[string]struct{}, len(def.InputColumns))
	for _, column := range def.InputColumns {
		if err := validateColumn(column, "input_columns"); err != nil {
			return err
		}
		if _, duplicate := seenInputs[column]; duplicate {
			return fmt.Errorf("input_columns contains duplicate column %q", column)
		}
		seenInputs[column] = struct{}{}
	}
	seenOutputs := make(map[string]struct{}, len(def.Outputs))
	for _, column := range def.Outputs {
		if err := validateColumn(column, "outputs"); err != nil {
			return err
		}
		if _, reserved := reservedColumns[column]; reserved {
			return fmt.Errorf("outputs contains reserved column %q", column)
		}
		if _, duplicate := seenOutputs[column]; duplicate {
			return fmt.Errorf("outputs contains duplicate column %q", column)
		}
		seenOutputs[column] = struct{}{}
	}
	return nil
}

// ValidateMembership checks a definition against one set's source columns, the
// other members' outputs and the result dataset's existing column origins.
// resultColumnOrigins maps a result column to the factor that owns it; a column
// owned by the same factor is reused rather than reported as a conflict.
func ValidateMembership(set FactorSet, def FactorDef, sourceColumns []string,
	siblings []FactorDef, resultColumnOrigins map[string]string) error {
	sourceSet := make(map[string]struct{}, len(sourceColumns))
	for _, column := range sourceColumns {
		sourceSet[strings.TrimSpace(column)] = struct{}{}
	}
	for _, column := range def.InputColumns {
		if _, exists := sourceSet[column]; !exists {
			return fmt.Errorf("unknown input column %q", column)
		}
	}
	for _, column := range def.Outputs {
		if _, collision := sourceSet[column]; collision {
			return fmt.Errorf("output column %q collides with a source column", column)
		}
		if origin, owned := resultColumnOrigins[column]; owned && origin != def.FactorID {
			return fmt.Errorf("output column %q is already owned by factor %q in result dataset", column, origin)
		}
	}
	for _, sibling := range siblings {
		if sibling.FactorID == def.FactorID {
			continue
		}
		for _, output := range sibling.Outputs {
			for _, column := range def.Outputs {
				if output == column {
					return fmt.Errorf("duplicate output %q in factor set %s", output, set.SetID)
				}
			}
		}
	}
	return nil
}

// InScope reports whether the subject belongs to this set's configured universe.
func (s FactorSet) InScope(subjectID string) bool {
	if s.SubjectMode == SubjectModeAll {
		return true
	}
	if s.SubjectMode != SubjectModeInclude {
		return false
	}
	subjectID = strings.TrimSpace(subjectID)
	if subjectID == "" {
		return false
	}
	for _, subject := range s.Subjects {
		if subject == subjectID {
			return true
		}
	}
	return false
}

func validateColumn(value, field string) error {
	if value != strings.TrimSpace(value) || !columnPattern.MatchString(value) {
		return fmt.Errorf("%s contains invalid column %q", field, value)
	}
	return nil
}

func validateJSONObject(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("params_json must be a JSON object")
	}
	var value map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&value); err != nil || value == nil {
		return fmt.Errorf("params_json must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("params_json must contain exactly one JSON object")
	}
	return nil
}
