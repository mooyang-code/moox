package engine

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// 数值的精度规范：输出的数字不带用不上的长尾小数。
const (
	// WeightPlaces 是目标权重、现金、gross、net、换手等占比的小数位数（万分之一）。目标权重向零截断而不是四舍五入：
	// 截断不会让现货权重之和超过 1、不会突破杠杆与单标的上限，零头留作现金。
	WeightPlaces = 4
	// ScoreDigits 是分数的有效数字位数：分数的量级随因子而变（收益率可能是 1e-5），按有效数字而不是固定小数位，
	// 小量级的分数不会被取整成 0。
	ScoreDigits = 6
)

// noise 是定点运算留下的舍入噪声的上界：截断前把绝对值加上它，免得本该恰好落在网格上的值（0.4）因为 0.39999…99 被截成 0.3999。
// rank 配权的份额与 cap 再分配用整数分配与精确有理数运算，剩下的噪声只来自 1/3 这类无法用定点数精确表示的份额参与的乘法
// （不超过集合大小 × 1e-18，集合上限一万个标的）。取 1e-13 留足余量。
var noise = quant.Must("0.0000000000001")

// quantizationTolerance 返回这份策略量化权重时容忍的噪声：只有所有配置的上限（各规则的 weight.total 与 weight.cap、组合的
// max_weight 与 leverage）都落在输出网格上时才容忍噪声——此时被噪声抬到网格点上的值不可能超过任何上限（超过上限的网格点
// 比上限至少大一格，原值早就超限了）。只要有一个上限在网格之外（例如 0.499999999999999999），就整体做精确的向零截断，
// 它只会让绝对值变小，不会突破任何上限。
func quantizationTolerance(program *dsl.Program) quant.Decimal {
	onGrid := func(value quant.Decimal) bool { return value.TruncateTo(WeightPlaces).Cmp(value) == 0 }
	portfolio := program.Strategy.Portfolio
	if !onGrid(portfolio.Leverage) || (portfolio.HasMaxWeight && !onGrid(portfolio.MaxWeight)) {
		return quant.Zero()
	}
	for i := range program.Rules {
		weight := program.Rules[i].Rule.Weight
		if !onGrid(weight.Total) || (weight.HasCap && !onGrid(weight.Cap)) {
			return quant.Zero()
		}
	}
	return noise
}

// truncateWeightWith 把权重向零截断到 WeightPlaces 位小数，先容忍 tolerance 以内的定点噪声（零表示精确的向零截断）。
func truncateWeightWith(value, tolerance quant.Decimal) quant.Decimal {
	if value.IsNegative() {
		return value.Sub(tolerance).TruncateTo(WeightPlaces)
	}
	return value.Add(tolerance).TruncateTo(WeightPlaces)
}

// formatScore 把分数格式化为 ScoreDigits 位有效数字。
func formatScore(value float64) string { return fmt.Sprintf("%.*g", ScoreDigits, value) }
