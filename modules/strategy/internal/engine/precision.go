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

// noise 是定点运算（除法、按比例再分配）留下的舍入噪声的上界：截断前把绝对值加上它，免得 0.4 因为 0.39999…99 被截成 0.3999。
// 只向外加这么一点点再截断（而不是先四舍五入到某一位）：四舍五入会把 0.9999999999999 这样合法的配置值进到 1，突破预算、
// 杠杆与单标的上限；加上 1e-15 的噪声后，离网格点 1e-15 以上的值仍然被截到网格下面。
var noise = quant.Must("0.000000000000001")

// truncateWeight 把权重向零截断到 WeightPlaces 位小数（先容忍 1e-15 以内的定点噪声）。
func truncateWeight(value quant.Decimal) quant.Decimal {
	if value.IsNegative() {
		return value.Sub(noise).TruncateTo(WeightPlaces)
	}
	return value.Add(noise).TruncateTo(WeightPlaces)
}

// formatScore 把分数格式化为 ScoreDigits 位有效数字。
func formatScore(value float64) string { return fmt.Sprintf("%.*g", ScoreDigits, value) }
