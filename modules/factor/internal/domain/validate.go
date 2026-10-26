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

// aliasPattern 是成员别名：与定义 ID 同一字符集，最长 32 个字符（列名“别名__参数名”要留出空间）。
var aliasPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

// categoryIDPattern 是分类 ID。
var categoryIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// ValidateAlias 检查成员别名。
func ValidateAlias(alias string) error {
	if !aliasPattern.MatchString(alias) || strings.Contains(alias, "__") {
		return fmt.Errorf("成员别名 %q 不合法：字母开头，只含字母、数字、下划线，最长 32 个字符，不能含连续下划线", alias)
	}
	return nil
}

// ValidateCategory 检查分类。
func ValidateCategory(category FactorCategory) error {
	if !categoryIDPattern.MatchString(category.CategoryID) {
		return fmt.Errorf("分类 ID %q 不合法：小写字母开头，只含小写字母、数字、下划线，最长 32 个字符", category.CategoryID)
	}
	return validateName(category.Name)
}

// ValidateTask checks the stable identity contract of a set.
func ValidateTask(set FactorTask) error {
	for name, value := range map[string]string{
		"task_id": set.TaskID, "space_id": set.SpaceID, "source_dataset_id": set.SourceDatasetID,
		"freq": set.Freq, "result_dataset_id": set.ResultDatasetID,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return fmt.Errorf("%s is required and must not have surrounding whitespace", name)
		}
	}
	if _, err := ParseFrequency(set.Freq); err != nil {
		return fmt.Errorf("invalid freq %q: %w", set.Freq, err)
	}
	if set.OwnerInstanceID != strings.TrimSpace(set.OwnerInstanceID) {
		return fmt.Errorf("owner_instance_id must not have surrounding whitespace")
	}
	wantTaskID := TaskID(set.SourceDatasetID, set.Freq)
	if set.OwnerInstanceID != "" {
		wantTaskID = InstanceTaskID(set.OwnerInstanceID)
	}
	if set.TaskID != wantTaskID {
		return fmt.Errorf("task_id must be %q", wantTaskID)
	}
	if set.ResultDatasetID != ResultDatasetID(set.TaskID) {
		return fmt.Errorf("result_dataset_id must be %q", ResultDatasetID(set.TaskID))
	}
	switch set.Status {
	case TaskStatusPending, TaskStatusEnabled, TaskStatusDisabled, TaskStatusDeleting:
		return nil
	default:
		return fmt.Errorf("invalid factor task status %q", set.Status)
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
	if !categoryIDPattern.MatchString(def.CategoryID) {
		return def, fmt.Errorf("category_id %q 不合法", def.CategoryID)
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
// FilledColumn 是因子表达式里可以引用的虚拟输入列（不属于源数据集的字段）：本地时序存储里该行是填充行时为 1，否则为 0。
// 默认把填充值当正常值参与计算，需要排除填充值的因子用它判断。与 serieswindow.FilledColumn 一致。
const FilledColumn = "is_filled"

// resultColumnOrigins maps a result column to the member alias that owns it; a
// column owned by the same member is reused rather than reported as a conflict.
// 输入列必须存在且为数值类型；params 与 let 的名称不能与源列重名（设计 4.2）。结果列包括输出列与参数列，
// 都不能与源列、其他成员的列或结果数据集里别的成员拥有的列重名。
func ValidateMembership(set FactorTask, member MemberDef, sourceColumns []SourceColumn,
	siblings []MemberDef, resultColumnOrigins map[string]string) error {
	if err := ValidateAlias(member.Alias); err != nil {
		return err
	}
	def, err := CompileDefinition(member.Factor)
	if err != nil {
		return fmt.Errorf("spec 编译失败：%w", err)
	}
	member.Factor = def
	sourceSet := make(map[string]SourceColumn, len(sourceColumns))
	for _, column := range sourceColumns {
		sourceSet[strings.TrimSpace(column.Name)] = column
	}
	for _, column := range def.InputColumns {
		if column == FilledColumn {
			continue // 虚拟列：该行是填充行（交易所缺数据的周期按规则填充，本地存储带标记）时为 1，否则为 0
		}
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
	own := memberColumns(member)
	for _, column := range own {
		if !validResultColumn(column) {
			return fmt.Errorf("结果列名 %q 不合法（别名加输出名或参数名太长，或不是合法标识符）", column)
		}
		if isReservedColumn(column) {
			return fmt.Errorf("结果列 %s 是系统列", column)
		}
		if _, collision := sourceSet[column]; collision {
			return fmt.Errorf("结果列 %q 与源列重名", column)
		}
		if origin, owned := resultColumnOrigins[column]; owned && origin != member.Alias {
			return fmt.Errorf("结果列 %q 已属于结果数据集里的成员 %q", column, origin)
		}
	}
	for _, sibling := range siblings {
		if sibling.Alias == member.Alias {
			continue
		}
		compiledSibling, err := CompileDefinition(sibling.Factor)
		if err != nil {
			continue // 兄弟定义编译失败时由它自己的校验报错
		}
		sibling.Factor = compiledSibling
		for _, column := range memberColumns(sibling) {
			for _, mine := range own {
				if column == mine {
					return fmt.Errorf("结果列 %q 在因子任务 %s 的成员 %s 与 %s 中重复", column, set.TaskID, sibling.Alias, member.Alias)
				}
			}
		}
	}
	return nil
}

// memberColumns 返回成员在结果数据集里的全部列：输出列与参数列。
func memberColumns(member MemberDef) []string {
	return append(member.OutputColumns(), member.ParamColumns()...)
}

var resultColumnPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

func validResultColumn(name string) bool { return resultColumnPattern.MatchString(name) }

func isReservedColumn(name string) bool {
	for _, reserved := range ReservedColumns {
		if name == reserved {
			return true
		}
	}
	return false
}
