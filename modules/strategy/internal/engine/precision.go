package engine

import (
	"fmt"

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
// rank 配权的份额与 cap 再分配现在用整数分配与精确有理数运算，剩下的噪声只来自 1/3 这类无法用定点数精确表示的份额
// （不超过集合大小 × 1e-18，集合上限一万个标的）。取 1e-13 留足余量；若这样会突破配置的精确上限，调用方退回精确截断。
var noise = quant.Must("0.0000000000001")

// truncateWeight 把权重向零截断到 WeightPlaces 位小数（先容忍 noise 以内的定点噪声）。
func truncateWeight(value quant.Decimal) quant.Decimal { return truncateWeightWith(value, noise) }

// truncateWeightWith 与 truncateWeight 相同，噪声容忍量由调用方给出（零表示精确的向零截断）。
func truncateWeightWith(value, tolerance quant.Decimal) quant.Decimal {
	if value.IsNegative() {
		return value.Sub(tolerance).TruncateTo(WeightPlaces)
	}
	return value.Add(tolerance).TruncateTo(WeightPlaces)
}

// formatScore 把分数格式化为 ScoreDigits 位有效数字。
func formatScore(value float64) string { return fmt.Sprintf("%.*g", ScoreDigits, value) }
