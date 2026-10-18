package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ReservedColumns = []string{"subject_id", "freq", "data_time", "series_tag"}

var factorIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
// 编译通过即为合法（设计 9.2）；allow_partial_universe 只用于截面因子。返回填好派生字段的定义。
func ValidateDefinition(def FactorDef) (FactorDef, error) {
	if !factorIDPattern.MatchString(def.FactorID) {
		return def, fmt.Errorf("factor_id must match [A-Za-z_][A-Za-z0-9_]*")
	}
	if err := validateName(def.Name); err != nil {
		return def, err
	}
	if strings.TrimSpace(def.Spec) == "" {
		return def, fmt.Errorf("spec is required")
	}
	if def.SpecHash != SpecHash(def.Spec) {
		return def, fmt.Errorf("spec_hash does not match spec")
	}
	compiled, err := CompileDefinition(def)
	if err != nil {
		return def, fmt.Errorf("spec 编译失败：%w", err)
	}
	if compiled.AllowPartialUniverse && compiled.FactorType != FactorTypeCrossSection {
		return def, fmt.Errorf("allow_partial_universe is only valid for %s factors", FactorTypeCrossSection)
	}
	return compiled, nil
}

// validateName 检查展示名：1～64 个字符，不含控制字符，首尾无空白。
func validateName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 64 {
		return fmt.Errorf("name 必须是 1～64 个字符，首尾不能有空白")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("name 不能包含控制字符")
		}
	}
	return nil
}

// SourceColumn 是源数据集的一列：名称与是否为数值类型（Storage 的 int、double）。
type SourceColumn struct {
	Name    string
	Numeric bool
}

// ValidateMembership checks a definition against one set's source columns, the
// other members' outputs and the result dataset's existing column origins.
// resultColumnOrigins maps a result column to the factor that owns it; a column
// owned by the same factor is reused rather than reported as a conflict.
// 输入列必须存在且为数值类型；params 与 let 的名称不能与源列重名（设计 4.2）。
func ValidateMembership(set FactorSet, def FactorDef, sourceColumns []SourceColumn,
	siblings []FactorDef, resultColumnOrigins map[string]string) error {
	def, err := CompileDefinition(def)
	if err != nil {
		return fmt.Errorf("spec 编译失败：%w", err)
	}
	sourceSet := make(map[string]SourceColumn, len(sourceColumns))
	for _, column := range sourceColumns {
		sourceSet[strings.TrimSpace(column.Name)] = column
	}
	for _, column := range def.InputColumns {
		source, exists := sourceSet[column]
		if !exists {
			return fmt.Errorf("unknown input column %q", column)
		}
		if !source.Numeric {
			return fmt.Errorf("input column %q is not numeric", column)
		}
	}
	if spec := def.compiled.Spec; spec != nil {
		for _, param := range spec.Params {
			if _, collision := sourceSet[param.Name]; collision {
				return fmt.Errorf("参数 %s 与源列重名", param.Name)
			}
		}
		for _, binding := range spec.Lets {
			if _, collision := sourceSet[binding.Name]; collision {
				return fmt.Errorf("中间量 %s 与源列重名", binding.Name)
			}
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
		compiledSibling, err := CompileDefinition(sibling)
		if err != nil {
			continue // 兄弟定义编译失败时由它自己的校验报错
		}
		for _, output := range compiledSibling.Outputs {
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
