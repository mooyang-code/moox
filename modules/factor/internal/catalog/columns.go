package catalog

import (
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorname"
)

// paramsColumnDisplayName 是 __params 列的展示名（Storage 对非因子输出列要求含汉字）。
const paramsColumnDisplayName = "因子参数"

// paramsColumn 是每个因子结果数据集都有的系统列 __params：JSON，记录每一行各成员实际用的参数。
func paramsColumn() storageio.ColumnInfo {
	return storageio.ColumnInfo{
		ColumnName: factorname.ParamsColumn, OriginType: storageio.ColumnOriginFactor, OriginID: factorname.ParamsColumn,
		ValueType: storageio.ColumnTypeJSON, Status: storageio.ColumnStatusActive,
		Attributes: map[string]string{"display_name": paramsColumnDisplayName, "column_kind": factorname.ColumnKindParams},
	}
}

// factorOutputColumns 返回成员的全部输出列的元数据（设计 5.3）：展示名等于列名；origin_id 是成员键；
// 属性记录这一列是什么——因子、成员、类型、输出名、分类、定义哈希，以及固定成员的完整参数（规范 JSON）。
func factorOutputColumns(member domain.MemberDef) []storageio.ColumnInfo {
	names := member.OutputColumns()
	columns := make([]storageio.ColumnInfo, 0, len(names))
	factorParams := ""
	if member.Kind == domain.MemberKindFixed {
		if text, err := factorname.CanonicalValues(member.Params); err == nil {
			factorParams = text
		}
	}
	for i, column := range names {
		attributes := map[string]string{
			"display_name": column, "column_kind": factorname.ColumnKindOutput,
			"factor_id": member.Factor.FactorID, "member_key": member.Key, "member_kind": member.Kind,
			"output_name": member.Factor.Outputs[i], "category_id": member.Factor.CategoryID,
			"definition_hash": domain.DefinitionHash(member.Factor),
		}
		if member.Kind == domain.MemberKindFixed {
			attributes["factor_params"] = factorParams
		}
		columns = append(columns, storageio.ColumnInfo{
			ColumnName: column, OriginType: storageio.ColumnOriginFactor, OriginID: member.Key,
			ValueType: storageio.ColumnTypeDouble, Status: storageio.ColumnStatusActive, Attributes: attributes,
		})
	}
	return columns
}

// appendFactorColumns 把成员的输出列和 __params 列追加到 columns（已存在的列名跳过）。
func appendFactorColumns(columns []storageio.ColumnInfo, members ...domain.MemberDef) []storageio.ColumnInfo {
	seen := make(map[string]struct{}, len(columns))
	for _, col := range columns {
		seen[col.ColumnName] = struct{}{}
	}
	add := func(column storageio.ColumnInfo) {
		if _, exists := seen[column.ColumnName]; exists {
			return
		}
		seen[column.ColumnName] = struct{}{}
		columns = append(columns, column)
	}
	add(paramsColumn())
	for _, member := range members {
		for _, column := range factorOutputColumns(member) {
			add(column)
		}
	}
	return columns
}
