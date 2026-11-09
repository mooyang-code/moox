package engine

import (
	"fmt"
	"math"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// select 的明细原因：group_full 是所属组名额已满而跳过，group_none 是分组列为 NaN、不属于任何组，select.then 是第一段选中、
// 二次排序没取到。
const (
	reasonGroupFull = "group_full"
	reasonGroupNone = "group_none"
	reasonThen      = "select.then"
)

// groupQuota 是 select.group 的名额：按名次从前往后逐个接受，所属组已满就跳过。
type groupQuota struct {
	key   map[string]float64
	limit func(group float64) int
	used  map[float64]int
}

// newGroupQuota 按候选（已按名次排好）建立分组名额；规则没写 group 时返回 nil。分组值取自本期行，NaN 与缺值不属于任何组。
// quotas 写法按组内最优名次给组排序：第 i 个出现的组名额是 quotas[i]，其余组是 rest。
// known 是本根没有分组值的标的沿用的分组值（hold 规则记下的冻结持仓的分组，见 frozenGroups），可为 nil。
func newGroupQuota(group *dsl.SelectGroup, ordered []string, rows map[string]Row, known map[string]float64) *groupQuota {
	if group == nil {
		return nil
	}
	quota := &groupQuota{key: make(map[string]float64, len(ordered)), used: map[float64]int{}}
	order := map[float64]int{}
	for _, id := range ordered {
		value, ok := groupValue(group, id, rows, known)
		if !ok {
			continue
		}
		quota.key[id] = value
		if _, seen := order[value]; !seen {
			order[value] = len(order)
		}
	}
	if group.Quotas == nil {
		quota.limit = func(float64) int { return group.Max }
	} else {
		quota.limit = func(value float64) int {
			if at := order[value]; at < len(group.Quotas) {
				return group.Quotas[at]
			}
			return group.Rest
		}
	}
	return quota
}

// groupValue 返回标的的分组值：本根行里的有效值，没有时取 known 里记下的值。
func groupValue(group *dsl.SelectGroup, id string, rows map[string]Row, known map[string]float64) (float64, bool) {
	if value, ok := rows[id].Values[group.By]; ok && !math.IsNaN(value) {
		return value, true
	}
	value, ok := known[id]
	return value, ok
}

// admit 尝试让标的占用所属组的一个名额；不能入选时返回原因。nil 表示没有分组限制。
func (q *groupQuota) admit(id string) string {
	if q == nil {
		return ""
	}
	value, ok := q.key[id]
	if !ok {
		return reasonGroupNone
	}
	if q.used[value] >= q.limit(value) {
		return reasonGroupFull
	}
	q.used[value]++
	return ""
}

// capShortNote 在 top/bottom 写比例、配了 cap、且 cap × 选中数小于 total 时返回摘要说明：保存时个数未知、没做这项检查，
// 运行时分不完的预算按 cap 语义留现金。个数为 0 与其余情况返回空串。
func capShortNote(rule *dsl.CompiledRule, count int) string {
	sel, weight := rule.Rule.Select, rule.Rule.Weight
	if !sel.IsRatio() || !weight.HasCap || count <= 0 {
		return ""
	}
	if weight.Cap.Mul(quant.Must(fmt.Sprint(count))).Cmp(weight.Total) >= 0 {
		return ""
	}
	return fmt.Sprintf("规则 %s 的 select 按比例换算出 %d 个，weight.cap %s × %d 小于 weight.total %s：分不完的预算留作现金", rule.Rule.ID, count, weight.Cap.String(), count, weight.Total.String())
}

// inRange 只保留名次落在 select.range 闭区间里的候选（保持原顺序）；没写 range 时原样返回。
func inRange(sel dsl.Select, ordered []string, rankOf map[string]int) []string {
	if !sel.HasRange() {
		return ordered
	}
	kept := make([]string, 0, len(ordered))
	for _, id := range ordered {
		if rank := rankOf[id]; rank >= sel.Range[0] && rank <= sel.Range[1] {
			kept = append(kept, id)
		}
	}
	return kept
}

// applyThen 在第一段选中的集合上按 select.then.score 重新排序，取 then.top 或 then.bottom 个；没写 then 时原样返回。
// 截面函数在选中集合上计算；分数无效的标的淘汰（全部无效时这条规则本期不出目标，不影响整期）。rejected 是第一段选中、最终没取到的标的与原因。
func applyThen(rule *dsl.CompiledRule, chosen []string, rows map[string]Row) (final []string, rejected map[string]string) {
	then := rule.Rule.Select.Then
	if then == nil || len(chosen) == 0 {
		return chosen, nil
	}
	scored := evaluateNumeric(rule.ThenScore, chosen, rows)
	rejected = make(map[string]string, len(chosen))
	for id, reason := range scored.failed {
		rejected[id] = reasonThen + ":" + reason
	}
	ordered := orderByRank(without(chosen, scored.failed), scored.values, then.Bottom > 0)
	count := then.Count(len(ordered))
	if count < len(ordered) {
		for _, id := range ordered[count:] {
			rejected[id] = reasonThen
		}
		ordered = ordered[:count]
	}
	return ordered, rejected
}
