package pipeline

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

// FactorValues 是一期里各标的各因子的输出值：标的 → 因子 → 输出名 → 值。无效值（NULL、NaN）不出现。
type FactorValues map[string]map[string]map[string]float64

// PeriodValues 取出计算结果里 period 这一期各标的各因子的输出值（回放用，不经过写入）。
// 时序因子按标的取行，截面因子的行自带标的；同一标的有多个序列时取标签排序靠后的（与写入时的覆盖顺序一致）。
func PeriodValues(plan Plan, computation Computation, period time.Time) (FactorValues, error) {
	out := FactorValues{}
	put := func(subject, factorID string, outputs []string, values []any) {
		if out[subject] == nil {
			out[subject] = map[string]map[string]float64{}
		}
		if out[subject][factorID] == nil {
			out[subject][factorID] = map[string]float64{}
		}
		for i, name := range outputs {
			if v, ok := numericOrNull(values[i]); ok && values[i] != nil {
				out[subject][factorID][name] = v
			}
		}
	}
	for _, factor := range plan.Factors {
		cross := factor.FactorType == domain.FactorTypeCrossSection
		for subject, item := range computation.Results[factor.FactorID] {
			if item.Err != nil {
				continue
			}
			if err := validateResultColumns(item, factor, cross); err != nil {
				return nil, fmt.Errorf("因子 %s 的结果列不符：%w", factor.FactorID, err)
			}
			for _, row := range item.Rows {
				at, _, rowSubject, values, err := decodeResultRow(row, factor, cross)
				if err != nil {
					return nil, fmt.Errorf("因子 %s 的结果行无法解码：%w", factor.FactorID, err)
				}
				if !at.Equal(period) {
					continue
				}
				if !cross {
					rowSubject = subject
				}
				put(rowSubject, factor.FactorID, factor.Outputs, values)
			}
		}
	}
	return out, nil
}
