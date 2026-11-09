package engine

import (
	"math"
	"strconv"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// reasonScaleInvalid 是仓位系数无效（NaN、无穷或求值出错）的缺数原因：系数无效的标的按缺数处理。
const reasonScaleInvalid = "scale_invalid"

// scaleDigits 是仓位系数换成定点数时保留的小数位数：浮点系数的二进制尾数不进入目标权重。
const scaleDigits = 12

// scaleOf 返回标的本根的仓位系数（截到 [0, max]）；规则没写 scale 时返回 1。系数无效时返回 false。
func scaleOf(rule *dsl.CompiledRule, id string, row Row) (float64, bool) {
	if rule.Scale == nil {
		return 1, true
	}
	value, err := rule.Scale.Run(rowEnv(rule.Scale, id, row, 0, nil))
	if err != nil {
		return 0, false
	}
	number, ok := toFloat(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return math.Min(math.Max(number, 0), rule.Rule.Scale.Max), true
}

// applyScale 把仓位系数乘到规则的输出权重上（规则内配权与 cap 之后），乘掉的部分留现金，不重新分配；规则状态（holding 批次的
// 基准权重、持有集合）在这之前已经算好，不含系数。系数为 0 的标的不出目标，明细仍记为入选、权重 0。
func applyScale(rule *dsl.CompiledRule, frame Frame, result *ruleResult, tolerance quant.Decimal) {
	if rule.Scale == nil {
		return
	}
	at := make(map[string]int, len(result.items))
	for i, item := range result.items {
		if item.Stage == StageWeighted {
			at[item.InstrumentID] = i
		}
	}
	for _, id := range sortedIDs(result.weights) {
		scale, _ := scaleOf(rule, id, frame.Rows[id])
		factor, err := quant.ParseLiteral(strconv.FormatFloat(scale, 'f', scaleDigits, 64))
		if err != nil {
			factor = quant.Zero()
		}
		weight := result.weights[id].Mul(factor)
		if weight.IsZero() {
			delete(result.weights, id)
		} else {
			result.weights[id] = weight
		}
		if i, ok := at[id]; ok {
			result.items[i].Weight = truncateWeightWith(weight, tolerance).String()
			result.items[i].Scale = formatScore(scale)
		}
	}
}
