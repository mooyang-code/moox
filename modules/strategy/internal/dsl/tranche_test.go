package dsl

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// trancheDSL 是一个 FH 形状的 tranche 规则（参数为合成值）：Po 选最大 1 个做多，后置过滤 wick ≤ p2 时清仓。
func trancheDSL(rows string, extra string) string {
	return `name: f1_demo
bar: 5m
rules:
  - id: demo
    type: tranche
    preset: f1
    weight: {total: 1}
    tranche:
      hold_period: "{h}"
      seal999: false
      long_factors:
        - {factor: "po__{s}_{l}", descending: true, weight: 1}
      short_factors: []
      long_weight: 2
      short_weight: 0
      long_coin_num: 1
      short_coin_num: 0
      filter_after:
        - {direction: long, factor: "wick__{n2}", type: value, op: lte, value: "{p2}"}
      stg_weight: "{w}"
    params:
      columns: [h, s, l, n2, p2, w]
      rows:
` + rows + extra + `
portfolio:
  leverage: 2
`
}

const trancheRows = `        - [1, 49, 145, 24, 0.1, 0.0033333333333333335]
        - [3, 49, 145, 42, 0.125, 0.0033333333333333335]
        - [6, 65, 193, 24, 0.05, 0.5]
`

func TestTrancheParseExpandsRows(t *testing.T) {
	strategy, err := Parse([]byte(trancheDSL(trancheRows, "")))
	require.NoError(t, err)
	rule := strategy.Rules[0]
	require.Equal(t, RuleTypeTranche, rule.Type)
	require.Len(t, rule.Tranche.Subs, 3)
	first := rule.Tranche.Subs[0]
	require.Equal(t, 1, first.Row)
	require.Equal(t, 1, first.HoldPeriod)
	require.Equal(t, ScoreRankSum, first.ScoreMethod)
	require.False(t, first.Seal999)
	require.Equal(t, []TrancheFactor{{Column: "po__49_145", Descending: true, Weight: 1}}, first.LongFactors)
	require.Equal(t, 2.0, first.LongWeight)
	require.Equal(t, 0.0, first.ShortCoinNum)
	require.Equal(t, 0.0033333333333333335, first.StgWeight)
	require.Len(t, first.FilterAfter, 1)
	filter := first.FilterAfter[0]
	require.Equal(t, "wick__24", filter.Column)
	require.Equal(t, TrancheFilterValue, filter.Type)
	require.Equal(t, -1e100, filter.Lo)
	require.Equal(t, 0.1, filter.Hi)
	require.True(t, filter.Inclusive)
	require.False(t, filter.Negate)
	require.Equal(t, 6, rule.Tranche.Subs[2].HoldPeriod)
	require.Equal(t, "po__65_193", rule.Tranche.Subs[2].LongFactors[0].Column)
	require.Equal(t, []string{"close", "po__49_145", "po__65_193", "volume", "wick__24", "wick__42"}, rule.TrancheColumns())

	// 排名签名只看影响选币的字段：前两行只差 h、n2、p2，同一组；第三行因子不同，另一组。
	groups := rule.Tranche.TrancheGroups()
	require.Len(t, groups, 2)
	sizes := []int{len(groups[0].Subs), len(groups[1].Subs)}
	require.ElementsMatch(t, []int{2, 1}, sizes)
}

func TestTrancheDefaultsFollowF1(t *testing.T) {
	strategy, err := Parse([]byte(`name: d
universe: {min_age_bars: 999}
rules:
  - id: d
    type: tranche
    preset: f1
    weight: {total: 1}
    tranche:
      long_factors: [{factor: a}]
      short_factors: [{factor: a}]
      filter_before:
        - {factor: b}
portfolio: {leverage: 2}
`))
	require.NoError(t, err)
	sub := strategy.Rules[0].Tranche.Subs[0]
	require.Equal(t, 12, sub.HoldPeriod)
	require.True(t, sub.Seal999)
	require.Equal(t, 1.0, sub.LongWeight)
	require.Equal(t, 1.0, sub.ShortWeight)
	require.Equal(t, 1.0, sub.LongCoinNum)
	require.Equal(t, 1.0, sub.StgWeight)
	require.Equal(t, TrancheFactor{Column: "a", Descending: true, Weight: 1}, sub.LongFactors[0])
	// F1FilterParams 的默认值：df1、value、lt、0.2。
	require.Equal(t, TrancheFilter{Column: "b", Type: TrancheFilterValue, Lo: -1e100, Hi: 0.2}, sub.FilterBefore[0])
}

func TestTrancheCompareNormalization(t *testing.T) {
	cases := []struct {
		op, value         string
		lo, hi            float64
		inclusive, negate bool
	}{
		{"lt", "0.5", -1e100, 0.5, false, false},
		{"lte", "0.5", -1e100, 0.5, true, false},
		{"gt", "0.5", 0.5, 1e100, false, false},
		{"gte", "0.5", 0.5, 1e100, true, false},
		{"bt", "[0.1, 0.9]", 0.1, 0.9, false, false},
		{"bte", "[0.1, 0.9]", 0.1, 0.9, true, false},
		// nb*：先把开闭取反再对结果取反（f1_utils：inclusive = not inclusive; reverse = '~'）。
		{"nbt", "[0.1, 0.9]", 0.1, 0.9, true, true},
		{"nbte", "[0.1, 0.9]", 0.1, 0.9, false, true},
		{"eq", "0.5", 0.5, 0.5, true, false},
		{"ne", "0.5", 0.5, 0.5, true, true},
	}
	for _, c := range cases {
		text := fmt.Sprintf(`name: c
rules:
  - id: c
    type: tranche
    preset: f1
    weight: 1
    tranche:
      seal999: false
      long_factors: [{factor: a}]
      short_weight: 0
      filter_before:
        - {direction: df1, factor: b, op: %s, value: %s}
`, c.op, c.value)
		strategy, err := Parse([]byte(text))
		require.NoError(t, err, c.op)
		got := strategy.Rules[0].Tranche.Subs[0].FilterBefore[0]
		require.Equal(t, c.lo, got.Lo, c.op)
		require.Equal(t, c.hi, got.Hi, c.op)
		require.Equal(t, c.inclusive, got.Inclusive, c.op)
		require.Equal(t, c.negate, got.Negate, c.op)
	}
}

func TestTrancheLogicGroups(t *testing.T) {
	strategy, err := Parse([]byte(`name: g
rules:
  - id: g
    type: tranche
    preset: f1
    weight: 1
    tranche:
      seal999: false
      long_factors: [{factor: a}]
      short_weight: 0
      filter_before:
        - logic: "1&(2|3)"
          items:
            - {direction: long, factor: a, op: gt, value: 0}
            - {direction: long, factor: b, type: rank, op: lte, value: 10, rank_ascending: true}
            - {direction: long, factor: c, type: pct, op: gte, value: 0.8}
`))
	require.NoError(t, err)
	group := strategy.Rules[0].Tranche.Subs[0].FilterBefore[0]
	require.Len(t, group.Items, 3)
	require.True(t, group.Items[1].RankAscending)
	require.True(t, group.Items[2].RankAscending, "pct 固定从小到大")
	tree := group.LogicTree()
	require.True(t, tree.Eval([]bool{true, false, true}))
	require.False(t, tree.Eval([]bool{false, true, true}))
	require.False(t, tree.Eval([]bool{true, false, false}))
	// & 优先于 |：1|2&3 = 1|(2&3)。
	logic, err := parseTrancheLogic("1|2&3", 3)
	require.NoError(t, err)
	require.True(t, logic.Eval([]bool{true, false, false}))
	require.False(t, logic.Eval([]bool{false, true, false}))
}

func TestTrancheRejects(t *testing.T) {
	base := func(f1 string) string {
		return `name: r
rules:
  - id: r
    type: tranche
    preset: f1
    weight: 1
    tranche:
      seal999: false
      long_factors: [{factor: a}]
      short_weight: 0
` + f1
	}
	cases := map[string]string{
		"hold_period 越界": base("      hold_period: 0\n"),
		"比较方式不认识":        base("      filter_after:\n        - {factor: b, op: le, value: 1}\n"),
		"区间写成数字":         base("      filter_after:\n        - {factor: b, op: bt, value: 1}\n"),
		"区间上下限颠倒":        base("      filter_after:\n        - {factor: b, op: bte, value: [2, 1]}\n"),
		"rank 用小数":       base("      filter_before:\n        - {factor: b, type: rank, op: lte, value: 1.5}\n"),
		"pct 超出 0～1":     base("      filter_before:\n        - {factor: b, type: pct, op: lte, value: 1.5}\n"),
		"coin_num 非整数":   base("      long_coin_num: 1.5\n"),
		"逻辑编号越界":         base("      filter_before:\n        - logic: \"1&3\"\n          items:\n            - {factor: a}\n            - {factor: b}\n"),
		"组内方向不同":         base("      filter_before:\n        - logic: \"1&2\"\n          items:\n            - {factor: a}\n            - {direction: short, factor: b}\n"),
		"多空因子缺失":         base("      short_weight: 1\n      short_coin_num: 1\n"),
	}
	for name, text := range cases {
		_, err := Parse([]byte(text))
		require.Error(t, err, name)
	}
	// 模板引用了参数表里没有的列。
	_, err := Parse([]byte(strings.Replace(trancheDSL(trancheRows, ""), `stg_weight: "{w}"`, `stg_weight: "{weight}"`, 1)))
	require.ErrorContains(t, err, "{weight}")
	// 行的列数不对。
	_, err = Parse([]byte(trancheDSL("        - [1, 49, 145, 24, 0.1]\n", "")))
	require.ErrorContains(t, err, "第 1 行")
	// 不能与 rank 规则混用。
	_, err = Parse([]byte(trancheDSL(trancheRows, `  - id: other
    type: rank
    score: "x"
    select: {top: 1}
    weight: {total: 0.5}
`)))
	require.ErrorContains(t, err, "只能全部是 tranche 规则")
	// seal999 为真时要求 min_age_bars ≥ 999。
	_, err = Parse([]byte("name: s\nrules:\n  - id: s\n    type: tranche\n    preset: f1\n    weight: 1\n    tranche:\n      long_factors: [{factor: a}]\n      short_weight: 0\n"))
	require.ErrorContains(t, err, "seal999")
	// rank 规则不能写 f1。
	_, err = Parse([]byte("name: x\nrules:\n  - id: x\n    type: rank\n    score: a\n    select: {top: 1}\n    weight: 1\n    tranche: {hold_period: 1}\n"))
	require.ErrorContains(t, err, "不是 tranche 规则")
}

func TestTrancheSizeLimit(t *testing.T) {
	var rows strings.Builder
	for i := 0; rows.Len() < 80<<10; i++ {
		fmt.Fprintf(&rows, "        - [%d, 49, 145, %d, 0.1, 0.001]\n", 1+i%36, 3+i)
	}
	text := trancheDSL(rows.String(), "")
	require.Greater(t, len(text), maxDSLBytes)
	_, err := Parse([]byte(text))
	require.NoError(t, err, "含 tranche 参数表时放宽到 1 MiB")
	padding := "# " + strings.Repeat("x", 70<<10) + "\n"
	_, err = Parse([]byte(padding + "name: p\nrules:\n  - id: p\n    type: rank\n    score: a\n    select: {top: 1}\n    weight: 1\n"))
	require.ErrorContains(t, err, "64 KiB")
}

func TestTrancheCompileChecksColumns(t *testing.T) {
	strategy, err := Parse([]byte(trancheDSL(trancheRows, "")))
	require.NoError(t, err)
	columns := []string{"close", "volume", "po__49_145", "po__65_193", "wick__24", "wick__42"}
	program, err := Compile(strategy, columns)
	require.NoError(t, err)
	require.Len(t, program.Rules[0].TrancheGroups, 2)
	require.Equal(t, []string{"close", "po__49_145", "po__65_193", "volume", "wick__24", "wick__42"}, program.Columns)
	_, err = Compile(strategy, columns[:5])
	require.ErrorContains(t, err, "wick__42")
	refs, err := ReferencedColumns(strategy)
	require.NoError(t, err)
	require.Contains(t, refs, "po__65_193")
}

func TestTrancheRejectsNonFiniteNumbers(t *testing.T) {
	for _, value := range []string{".nan", ".inf", "-.inf", "!!float NaN"} {
		text := "name: n\nrules:\n  - id: n\n    type: tranche\n    preset: f1\n    weight: 1\n    tranche:\n      seal999: false\n      long_factors: [{factor: a}]\n      short_weight: 0\n      stg_weight: " + value + "\n"
		_, err := Parse([]byte(text))
		require.ErrorContains(t, err, "有限的数字", value)
	}
}
