package engine

import (
	"fmt"
	"math"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// 比较方式 × 区间边界 × NaN 的独立求值表（不串联，避免前面的条件掩盖后面的边界）。期望值按参考实现：
// pandas between(lo, hi, inclusive) 对 NaN 为假，nb* 与 ne 再取反，所以 NaN 在 nb* 与 ne 下为真。
func TestTrancheConditionTable(t *testing.T) {
	nan := math.NaN()
	values := []float64{0.1, 0.2, 0.5, 0.8, 0.9, nan}
	cases := []struct {
		op    string
		value string
		want  []bool // 对应 values
	}{
		{"lt", "0.5", []bool{true, true, false, false, false, false}},
		{"lte", "0.5", []bool{true, true, true, false, false, false}},
		{"gt", "0.5", []bool{false, false, false, true, true, false}},
		{"gte", "0.5", []bool{false, false, true, true, true, false}},
		{"bt", "[0.2, 0.8]", []bool{false, false, true, false, false, false}},
		{"bte", "[0.2, 0.8]", []bool{false, true, true, true, false, false}},
		{"nbt", "[0.2, 0.8]", []bool{true, false, false, false, true, true}},
		{"nbte", "[0.2, 0.8]", []bool{true, true, false, true, true, true}},
		{"eq", "0.5", []bool{false, false, true, false, false, false}},
		{"ne", "0.5", []bool{true, true, false, true, true, true}},
	}
	ids := make([]string, len(values))
	for i := range ids {
		ids[i] = fmt.Sprintf("S%d-USDT", i)
	}
	lookup := func(id string) float64 {
		var index int
		fmt.Sscanf(id, "S%d-USDT", &index)
		return values[index]
	}
	for _, c := range cases {
		text := fmt.Sprintf("name: c\nrules:\n  - id: c\n    type: tranche\n    weight: 1\n    tranche:\n      hold_period: 12\n      hold_count: candidate_rows\n      score: {method: rank_sum, missing: median}\n      sizing: {method: quantity, scale: 0.5}\n      long_factors: [{factor: a}]\n      short_weight: 0\n      filter_before:\n        - {factor: b, op: %s, value: %s}\n", c.op, c.value)
		strategy, err := dsl.Parse([]byte(text))
		require.NoError(t, err, c.op)
		got := trancheCondition(ids, lookup, strategy.Rules[0].Tranche.Subs[0].FilterBefore[0])
		require.Equal(t, c.want, got, c.op)
	}
}

// rank 与 pct 的 method='first'：并列按样本顺序先到先得，NaN 不参与排名、比较为假。
func TestTrancheRankFirst(t *testing.T) {
	nan := math.NaN()
	require.Equal(t, []float64{1, 3, 2, 4}, firstRanks([]float64{1, 2, 1, 5}, true, false))
	// pandas Series([1, 2, 1, 5]).rank(method="first", ascending=False) == [3, 2, 4, 1]
	require.Equal(t, []float64{3, 2, 4, 1}, firstRanks([]float64{1, 2, 1, 5}, false, false))
	ranks := firstRanks([]float64{3, nan, 1, 2}, true, true)
	require.Equal(t, 1.0, ranks[0])
	require.True(t, math.IsNaN(ranks[1]))
	require.InDelta(t, 1.0/3, ranks[2], 1e-15)
	require.Equal(t, []float64{1.5, 1.5, 3}, averageRanks([]float64{2, 2, 7}))
	require.Equal(t, []float64{2, 2, 3, 1}, fillMedian([]float64{nan, 2, 3, 1}))
}
