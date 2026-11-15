package pipeline

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// FactorValues 是一期里各序列各成员的输出值：序列（标的, series_tag）→ 成员 → 结果列名 → 值。空值（NULL）不出现。
type FactorValues map[seriesset.Series]map[string]map[string]float64

// PeriodValues 取出计算结果里 period 这一期各序列各成员的输出值（回放用，不经过写入）。
// 时序因子按标的取行、行里带 series_tag，截面因子的行自带标的与 series_tag；统一数据集上同一标的的两条序列各自一项。
func PeriodValues(plan Plan, computation Computation, period time.Time) (FactorValues, error) {
	out := FactorValues{}
	put := func(series seriesset.Series, member string, outputs []string, values []any) {
		if out[series] == nil {
			out[series] = map[string]map[string]float64{}
		}
		if out[series][member] == nil {
			out[series][member] = map[string]float64{}
		}
		for i, name := range outputs {
			if v, ok := numericOrNull(values[i]); ok && values[i] != nil {
				out[series][member][name] = v
			}
		}
	}
	for _, member := range plan.Members {
		cross := member.Factor.FactorType == domain.FactorTypeCrossSection
		for subject, item := range computation.Results[member.Key] {
			if item.Err != nil {
				continue
			}
			if err := validateResultColumns(item, member, cross); err != nil {
				return nil, fmt.Errorf("成员 %s 的结果列不符：%w", member.Key, err)
			}
			for _, row := range item.Rows {
				at, tag, rowSubject, values, err := decodeResultRow(row, cross)
				if err != nil {
					return nil, fmt.Errorf("成员 %s 的结果行无法解码：%w", member.Key, err)
				}
				if !at.Equal(period) {
					continue
				}
				if !cross {
					rowSubject = subject
				}
				put(seriesset.Series{SubjectID: rowSubject, SeriesTag: tag}, member.Key, member.OutputColumns(), values)
			}
		}
	}
	return out, nil
}
