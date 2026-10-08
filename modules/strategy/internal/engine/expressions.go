package engine

import (
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
		return false, err
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

// evaluateNumeric 在样本 ids 上执行数值表达式。截面函数先对样本做预计算，内层表达式递归求值；
// 任一环节失败的标的被记为失败并从后续计算中剔除。
func evaluateNumeric(expression *dsl.Expression, ids []string, rows map[string]Row) numericResult {
	result := numericResult{values: make(map[string]float64, len(ids)), failed: make(map[string]string)}
	vars := make(map[string]map[string]float64, len(ids))
	for _, id := range ids {
		vars[id] = make(map[string]float64, len(expression.Normalizers))
	}
	live := append([]string(nil), ids...)
	for _, normalizer := range expression.Normalizers {
		inner := evaluateNumeric(normalizer.Inner, live, rows)
		for id, reason := range inner.failed {
			result.failed[id] = reason
		}
		live = without(live, inner.failed)
		var normalized map[string]float64
		switch normalizer.Kind {
		case "rank":
			normalized = percentileRank(live, inner.values)
		default:
			normalized = zScore(live, inner.values)
		}
		for id, value := range normalized {
			vars[id][normalizer.Variable] = value
		}
	}
	for _, id := range live {
		value, err := expression.Run(rowEnv(id, rows[id], 0, vars[id]))
		if err != nil {
			result.failed[id] = "score_error:" + err.Error()
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

// zScore 是截面标准化：总体标准差为 0 时记 0。
func zScore(ids []string, values map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(ids))
	if len(ids) == 0 {
		return result
	}
	mean := 0.0
	for _, id := range ids {
		mean += values[id]
	}
	mean /= float64(len(ids))
	variance := 0.0
	for _, id := range ids {
		delta := values[id] - mean
		variance += delta * delta
	}
	variance /= float64(len(ids))
	deviation := math.Sqrt(variance)
	for _, id := range ids {
		if deviation == 0 {
			result[id] = 0
			continue
		}
		result[id] = (values[id] - mean) / deviation
	}
	return result
}
