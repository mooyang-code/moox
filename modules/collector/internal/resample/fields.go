package resample

import (
	"fmt"
	"sort"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// ReduceOp 描述一根目标 K 线里某一列如何从桶内源 K 线归约得到。
type ReduceOp string

const (
	ReduceFirst         ReduceOp = "first"
	ReduceMax           ReduceOp = "max"
	ReduceMin           ReduceOp = "min"
	ReduceLast          ReduceOp = "last"
	ReduceSum           ReduceOp = "sum"
	ReduceSumAllPresent ReduceOp = "sum_all_present"
	ReduceSumInt        ReduceOp = "sum_int"
)

// FieldRule 描述一个标准可重采样列的契约：列名、值类型、归约算子、是否源数据集必选、是否允许源行为空、是否非负。
type FieldRule struct {
	Name        string
	ValueType   storagepb.FieldValueType
	Op          ReduceOp
	Required    bool
	Nullable    bool
	NonNegative bool
}

// FieldValue 是重采样中一列的标量值（浮点、整数或 NULL）。
type FieldValue struct {
	Float float64
	Int   int64
	Null  bool
}

// StandardFieldRules 是重采样支持的标准列归约规则表（按稳定顺序输出）。
// 后续扩充新的可聚合标准列（如 open_interest: Last）只需在此表新增一行并在 taskresult.KlineOutputFields 登记显示元信息。
var StandardFieldRules = []FieldRule{
	{Name: fieldOpen, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Op: ReduceFirst, Required: true},
	{Name: fieldHigh, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Op: ReduceMax, Required: true},
	{Name: fieldLow, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Op: ReduceMin, Required: true},
	{Name: fieldClose, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Op: ReduceLast, Required: true},
	{Name: fieldVolume, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Op: ReduceSum, Required: true, NonNegative: true},
	{Name: fieldQuoteVolume, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Op: ReduceSum, NonNegative: true},
	{Name: fieldTradeNum, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Op: ReduceSumInt, NonNegative: true},
	{Name: fieldAmount, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Op: ReduceSumAllPresent, Nullable: true, NonNegative: true},
}

// StandardFieldRule 按列名查找标准归约规则（大小写不敏感）。
func StandardFieldRule(name string) (FieldRule, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	for _, rule := range StandardFieldRules {
		if rule.Name == key {
			return rule, true
		}
	}
	return FieldRule{}, false
}

// AllOutputFieldNames 按标准归约规则表顺序返回重采样支持的全部标准列名（供数据类型规格与表单兜底使用）。
func AllOutputFieldNames() []string {
	out := make([]string, len(StandardFieldRules))
	for i, rule := range StandardFieldRules {
		out[i] = rule.Name
	}
	return out
}

// AvailableRulesForSource 将标准字段归约规则表与源数据集活跃列求交，返回可合成的列规则（保持规则表顺序）。
// 当 columnTypes 为 nil（调用方未提供列发现能力）时，默认按必选 5 列 OHLCV 返回；
// 当 columnTypes 非 nil 时，必须包含全部必选 5 列（open/high/low/close/volume）且任意命中的标准列类型必须匹配。
func AvailableRulesForSource(columnTypes map[string]storagepb.FieldValueType) ([]FieldRule, error) {
	if columnTypes == nil {
		out := make([]FieldRule, 0, 5)
		for _, rule := range StandardFieldRules {
			if rule.Required {
				out = append(out, rule)
			}
		}
		return out, nil
	}
	normalized := make(map[string]storagepb.FieldValueType, len(columnTypes))
	for name, valueType := range columnTypes {
		key := strings.ToLower(strings.TrimSpace(name))
		if key != "" {
			normalized[key] = valueType
		}
	}
	var missing []string
	out := make([]FieldRule, 0, len(StandardFieldRules))
	for _, rule := range StandardFieldRules {
		actualType, ok := normalized[rule.Name]
		if !ok {
			if rule.Required {
				missing = append(missing, rule.Name)
			}
			continue
		}
		if actualType != rule.ValueType {
			return nil, fmt.Errorf("source Dataset column %s has value_type=%s, want %s", rule.Name, actualType.String(), rule.ValueType.String())
		}
		out = append(out, rule)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("source Dataset missing active K-line columns: %s", strings.Join(missing, ","))
	}
	return out, nil
}

// ResolveTaskRules 根据源数据集活跃列与任务指定的 output_fields，解析本次重采样实际读写与归约的列规则列表。
// outputFields 为空时输出源数据集全部可合成列；非空时校验每一列均在源数据集可合成列中。
func ResolveTaskRules(columnTypes map[string]storagepb.FieldValueType, outputFields []string) ([]FieldRule, error) {
	available, err := AvailableRulesForSource(columnTypes)
	if err != nil {
		return nil, err
	}
	if len(outputFields) == 0 {
		return available, nil
	}
	byName := make(map[string]FieldRule, len(available))
	allowedNames := make([]string, 0, len(available))
	for _, rule := range available {
		byName[rule.Name] = rule
		allowedNames = append(allowedNames, rule.Name)
	}
	selected := make(map[string]struct{}, len(outputFields))
	for _, raw := range outputFields {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := byName[name]; !ok {
			if _, isStandard := StandardFieldRule(name); isStandard {
				return nil, fmt.Errorf("源数据集不含可合成输出字段 %s（当前源数据集可选：%s）", name, strings.Join(allowedNames, "、"))
			}
			return nil, fmt.Errorf("重采样不能合成输出字段 %s（当前源数据集可选：%s）", name, strings.Join(allowedNames, "、"))
		}
		selected[name] = struct{}{}
	}
	if len(selected) == 0 {
		return available, nil
	}
	out := make([]FieldRule, 0, len(selected))
	for _, rule := range available {
		if _, ok := selected[rule.Name]; ok {
			out = append(out, rule)
		}
	}
	return out, nil
}

// ValidateOutputFieldsBasic 在尚未加载源数据集列时做基础校验：只允许标准字段归约规则表中的字段。
func ValidateOutputFieldsBasic(outputFields []string) error {
	if len(outputFields) == 0 {
		return nil
	}
	allowedNames := AllOutputFieldNames()
	for _, raw := range outputFields {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := StandardFieldRule(name); !ok {
			return fmt.Errorf("重采样不能合成输出字段 %s（支持的可归约字段：%s）", name, strings.Join(allowedNames, "、"))
		}
	}
	return nil
}
