package catalog

import (
	"errors"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 因子结果数据集里来源为 FACTOR 的列分两类，由列属性 column_kind 区分（设计「因子任务与参数模型重构设计」5.3、5.5）：
//   - output：成员的一个输出，值类型 double；
//   - params：每个结果数据集恰好一列 __params，值类型 json，记录每一行各成员实际用的参数。
//
// Storage 只比较字符串，不引用因子命名包：它不理解因子，也不解析 __params 里的内容。
const (
	factorColumnKindOutput = "output"
	factorColumnKindParams = "params"
	factorParamsColumnName = "__params"
	factorSegmentSep       = "__"
)

// factorColumnKind 返回因子列的 column_kind；缺失时为空。
func factorColumnKind(attrs map[string]string) string {
	return strings.TrimSpace(attrs["column_kind"])
}

// validateFactorResultColumn 校验因子结果数据集里来源为 FACTOR 的列。
func validateFactorResultColumn(item *pb.DatasetColumn) error {
	attrs := item.GetAttributes()
	name := strings.TrimSpace(item.GetColumnName())
	switch kind := factorColumnKind(attrs); kind {
	case factorColumnKindParams:
		if name != factorParamsColumnName || strings.TrimSpace(item.GetOriginId()) != factorParamsColumnName {
			return fmt.Errorf("factor result params column must be named %s", factorParamsColumnName)
		}
		if item.GetValueType() != pb.FieldValueType_FIELD_VALUE_TYPE_JSON {
			return fmt.Errorf("factor result column %s must be json", factorParamsColumnName)
		}
		return nil
	case factorColumnKindOutput:
		return validateFactorOutputColumn(item, attrs, name)
	default:
		return fmt.Errorf("factor result columns require column_kind %s or %s, got %q", factorColumnKindOutput, factorColumnKindParams, kind)
	}
}

// validateFactorOutputColumn 校验输出列：属性齐全，列名等于成员键（输出名就是因子 ID 的单输出因子）或 成员键__输出名（多输出因子）。
func validateFactorOutputColumn(item *pb.DatasetColumn, attrs map[string]string, name string) error {
	factorID := strings.TrimSpace(attrs["factor_id"])
	memberKey := strings.TrimSpace(attrs["member_key"])
	output := strings.TrimSpace(attrs["output_name"])
	memberKind := strings.TrimSpace(attrs["member_kind"])
	if factorID == "" || memberKey == "" || output == "" {
		return errors.New("factor result output columns require factor_id, member_key and output_name")
	}
	if memberKind != "fixed" && memberKind != "var" {
		return fmt.Errorf("factor result output column member_kind must be fixed or var, got %q", memberKind)
	}
	if memberKey != factorID && !strings.HasPrefix(memberKey, factorID+factorSegmentSep) {
		return fmt.Errorf("factor result member_key %q must start with factor_id %q", memberKey, factorID)
	}
	want := memberKey
	if output != factorID {
		want = memberKey + factorSegmentSep + output
	}
	if name != want {
		return fmt.Errorf("factor result output column must be named %q, got %q", want, name)
	}
	if strings.TrimSpace(item.GetOriginId()) != memberKey {
		return fmt.Errorf("factor result output column origin_id must be member_key %q", memberKey)
	}
	if item.GetValueType() != pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE {
		return errors.New("factor result output columns must be double")
	}
	return nil
}
