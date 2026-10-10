package engine

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// rowEnv 构造一行的表达式环境：列值、截面预计算变量、score、instrument_id 与 bars。
func rowEnv(id string, row Row, score float64, vars map[string]float64) map[string]any {
	env := make(map[string]any, len(row.Values)+len(vars)+3)
	for column, value := range row.Values {
		env[column] = value
	}
	for name, value := range vars {
		env[name] = value
	}
	env["instrument_id"] = id
	env["score"] = score
	previous := row.Previous
	if previous == nil {
		previous = map[string]float64{}
	}
	env["bars"] = []map[string]float64{row.Values, previous}
	return env
}

// runBool 对一行执行布尔表达式。
func runBool(expression *dsl.Expression, id string, row Row, score float64) (bool, error) {
	value, err := expression.Run(rowEnv(id, row, score, nil))
	if err != nil {
		return false, errExprRuntime
	}
	result, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("表达式返回 %T，需要布尔值", value)
	}
	return result, nil
}

// numericResult 是一个样本上数值表达式的结果：每个标的的值，或失败原因。
type numericResult struct {
	values map[string]float64
	failed map[string]string
}

// evaluateNumeric 在样本 ids 上执行数值表达式。同一层的截面函数在同一个样本上计算：先在该样本上求出全部参数
// （参数中嵌套的截面函数按同样的规则在下一层计算），任一参数无效的标的从这一层的名次与标准化中剔除；
// 外层结果无效（例如除以名次 0）的标的只淘汰自身，不触发重算。每层只计算一次，结果与书写顺序无关。
func evaluateNumeric(expression *dsl.Expression, ids []string, rows map[string]Row) numericResult {
	result := numericResult{values: make(map[string]float64, len(ids)), failed: make(map[string]string)}
	inners := make([]numericResult, len(expression.Normalizers))
	for i, normalizer := range expression.Normalizers {
		inners[i] = evaluateNumeric(normalizer.Inner, ids, rows)
		for _, id := range ids {
			if reason, failed := inners[i].failed[id]; failed {
				if _, seen := result.failed[id]; !seen {
					result.failed[id] = reason
				}
			}
		}
	}
	sample := without(ids, result.failed)
	vars := make(map[string]map[string]float64, len(sample))
	for _, id := range sample {
		vars[id] = make(map[string]float64, len(expression.Normalizers))
	}
	for i, normalizer := range expression.Normalizers {
		var normalized map[string]float64
		switch normalizer.Kind {
		case "rank":
			normalized = percentileRank(sample, inners[i].values)
		default:
			normalized = zScore(sample, inners[i].values)
		}
		for id, value := range normalized {
			vars[id][normalizer.Variable] = value
		}
	}
	for _, id := range sample {
		value, err := expression.Run(rowEnv(id, rows[id], 0, vars[id]))
		if err != nil {
			result.failed[id] = scoreRuntimeReason
			continue
		}
		number, ok := toFloat(value)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
			result.failed[id] = "score_invalid"
			continue
		}
		result.values[id] = number
	}
	return result
}

// errExprRuntime 与 scoreRuntimeReason 是表达式求值出错的中文说明；表达式库的英文原文不进入结果记录。保存与编译时已经
// 拒绝了会在求值时必然出错的写法（?? 与范围、分支类型不同的三元表达式、写错的正则等），DSL 的限制又让其余运行错误
// 不会发生，这里只是兜底。
var errExprRuntime = errors.New("表达式求值出错")

// scoreRuntimeReason 是分数表达式运行出错时的明细原因 score_error:<中文原因>。
const scoreRuntimeReason = "score_error:求值出错"

func without(ids []string, failed map[string]string) []string {
	if len(failed) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := failed[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	default:
		return 0, false
	}
}

// percentileRank 是截面百分位名次：按值升序、同分取平均名次、单样本记 0.5，结果在 [0, 1]。
func percentileRank(ids []string, values map[string]float64) map[string]float64 {
	type pair struct {
		id    string
		value float64
	}
	pairs := make([]pair, 0, len(ids))
	for _, id := range ids {
		pairs = append(pairs, pair{id: id, value: values[id]})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].value != pairs[j].value {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].id < pairs[j].id
	})
	result := make(map[string]float64, len(pairs))
	if len(pairs) == 1 {
		result[pairs[0].id] = 0.5
		return result
	}
	for i := 0; i < len(pairs); {
		j := i + 1
		for j < len(pairs) && pairs[j].value == pairs[i].value {
			j++
		}
		rank := (float64(i+1) + float64(j)) / 2
		for k := i; k < j; k++ {
			result[pairs[k].id] = (rank - 1) / float64(len(pairs)-1)
		}
		i = j
	}
	return result
}

// zScore 是截面标准化：样本值全部相同、或标准差相对量级小到只剩浮点误差时，全部记 0。先按样本的最大绝对值缩放到 [-1, 1]
// 再算均值与方差（z 值与缩放无关）：直接累加偏差平方会在量级接近 1e154 时溢出成 Inf，有限的偏差除以 Inf 得 0，
// 悄悄把所有标的的分数变成相同的 0。
func zScore(ids []string, values map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(ids))
	if len(ids) == 0 {
		return result
	}
	scale := 0.0
	for _, id := range ids {
		scale = math.Max(scale, math.Abs(values[id]))
	}
	if scale == 0 || math.IsInf(scale, 0) || math.IsNaN(scale) {
		// 全 0 无离散度；含 Inf、NaN 的样本交给上层的非有限值检查，这里不制造有限的分数。
		for _, id := range ids {
			result[id] = 0
		}
		if math.IsInf(scale, 0) || math.IsNaN(scale) {
			for _, id := range ids {
				result[id] = math.NaN()
			}
		}
		return result
	}
	mean := 0.0
	for _, id := range ids {
		mean += values[id] / scale
	}
	mean /= float64(len(ids))
	variance := 0.0
	for _, id := range ids {
		delta := values[id]/scale - mean
		variance += delta * delta
	}
	variance /= float64(len(ids))
	deviation := math.Sqrt(variance)
	flat := deviation <= zeroVarianceTolerance
	for _, id := range ids {
		if flat {
			result[id] = 0
			continue
		}
		result[id] = (values[id]/scale - mean) / deviation
	}
	return result
}

// zeroVarianceTolerance 是零方差的相对容差：标准差不超过样本量级的 1e-12 视为浮点误差。
const zeroVarianceTolerance = 1e-12
