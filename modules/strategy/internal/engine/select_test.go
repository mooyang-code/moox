package engine

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rankedRows 构造 n 个标的 S01…Sn，分数 m 依次为 n…1（S01 最高）；extra 给每个标的追加列值。
func rankedRows(n int, extra func(i int) map[string]float64) map[string]Row {
	rows := make(map[string]Row, n)
	for i := 1; i <= n; i++ {
		values := map[string]float64{"m": float64(n - i + 1)}
		if extra != nil {
			for column, value := range extra(i) {
				values[column] = value
			}
		}
		rows[fmt.Sprintf("S%02d", i)] = Row{Values: values}
	}
	return rows
}

func selectRule(selectBody string, extra ...string) string {
	text := "name: s\nrules:\n  - id: r\n    type: rank\n    score: \"m\"\n    select: " + selectBody + "\n    weight: {total: 1}\n"
	for _, line := range extra {
		text += "    " + line + "\n"
	}
	return text + "portfolio: {max_missing: 1}\n"
}

func weightedIDs(decision Decision) []string {
	ids := make([]string, 0, len(decision.Targets))
	for _, target := range decision.Targets {
		ids = append(ids, target.InstrumentID)
	}
	sortStringSlice(ids)
	return ids
}

// 比例：分母是通过 filter 且分数有效的样本数（这里 25 个），三种取整方式与 count_min。
func TestSelectRatioRounding(t *testing.T) {
	cases := []struct {
		body string
		want int
	}{
		{"{top: 0.1}", 2},                        // floor(2.5)
		{"{top: 0.1, count_round: floor}", 2},    // 同默认
		{"{top: 0.1, count_round: ceil}", 3},     // ceil(2.5)
		{"{top: 0.1, count_round: round}", 3},    // round(2.5) 进位
		{"{top: 0.12, count_round: round}", 3},   // round(3.0) 恰好整数
		{"{top: 0.12, count_round: ceil}", 3},    // 0.12 × 25 = 3 精确，不因浮点多取
		{"{top: 0.01, count_min: 2}", 2},         // floor(0.25)=0，取 count_min
		{"{bottom: 0.2, count_round: floor}", 5}, // bottom 同理
		{"{top: 0.5, count_min: 1}", 12},         // count_min 小于换算值时不起作用
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			program := compile(t, selectRule(tc.body), "m")
			decision := evaluate(t, program, frameOf(program, rankedRows(25, nil)), State{})
			assertOK(t, decision)
			require.Len(t, decision.Targets, tc.want)
		})
	}
}

// 比例的分母不含被 filter 淘汰与分数无效的标的，在 select.where 之前计数。
func TestSelectRatioDenominatorAfterFilterBeforeWhere(t *testing.T) {
	program := compile(t, `name: s
rules:
  - id: r
    type: rank
    filter: "ok > 0"
    score: "m"
    select: {top: 0.5, where: "score > 7"}
    weight: {total: 1}
portfolio: {max_missing: 1}
`, "ok", "m")
	// 10 个标的，S09、S10 被 filter 淘汰，样本 8 个 → 取 4 个；where 只放行 m > 7 的 S01～S03，最终 3 个。
	rows := rankedRows(10, func(i int) map[string]float64 {
		if i >= 9 {
			return map[string]float64{"ok": 0}
		}
		return map[string]float64{"ok": 1}
	})
	decision := evaluate(t, program, frameOf(program, rows), State{})
	assertOK(t, decision)
	require.Equal(t, []string{"S01", "S02", "S03"}, weightedIDs(decision))
}

// range 按名次闭区间选，含两端；越过样本的部分为空。
func TestSelectRangeBounds(t *testing.T) {
	program := compile(t, selectRule("{range: [3, 5]}"), "m")
	decision := evaluate(t, program, frameOf(program, rankedRows(8, nil)), State{})
	assertOK(t, decision)
	require.Equal(t, []string{"S03", "S04", "S05"}, weightedIDs(decision))
	require.Equal(t, "not_selected", findItem(t, decision, "r", "S02").Reason)
	require.Equal(t, "not_selected", findItem(t, decision, "r", "S06").Reason)

	tail := compile(t, selectRule("{range: [7, 12]}"), "m")
	decision = evaluate(t, tail, frameOf(tail, rankedRows(8, nil)), State{})
	assertOK(t, decision)
	require.Equal(t, []string{"S07", "S08"}, weightedIDs(decision))
}

// group.max：每组最多 max 个，按名次逐个接受，组满跳过并记 group_full；分组值为 NaN 的不入选。
func TestSelectGroupMaxAndNaN(t *testing.T) {
	program := compile(t, selectRule("{top: 4, group: {by: g, max: 2}}"), "m", "g")
	// 组：S01～S03 属于 1，S04 为 NaN，S05、S06 属于 2。
	rows := rankedRows(6, func(i int) map[string]float64 {
		switch {
		case i <= 3:
			return map[string]float64{"g": 1}
		case i == 4:
			return map[string]float64{"g": math.NaN()}
		default:
			return map[string]float64{"g": 2}
		}
	})
	decision := evaluate(t, program, frameOf(program, rows), State{})
	assertOK(t, decision)
	require.Equal(t, []string{"S01", "S02", "S05", "S06"}, weightedIDs(decision))
	require.Equal(t, reasonGroupFull, findItem(t, decision, "r", "S03").Reason)
	require.Equal(t, reasonGroupNone, findItem(t, decision, "r", "S04").Reason)
}

// group.by 写来源列（来源名.列名）：分组值取行里并进来的来源列，名额、group_full、group_none 与主帧列一致；
// 行里没有这个来源列的标的按缺数处理（missing:<来源名.列名>），不参与排名、不占名额。
func TestSelectGroupBySourceColumn(t *testing.T) {
	program := compile(t, "name: s\nsources:\n  daily: {bar: 1d}\nrules:\n  - id: r\n    type: rank\n    score: \"m\"\n"+
		"    select: {top: 4, group: {by: daily.g, max: 2}}\n    weight: {total: 1}\nportfolio: {max_missing: 1}\n", "m", "daily.g")
	require.Equal(t, []string{"daily.g", "m"}, program.Rules[0].Columns())
	// 组：S01 没有来源列（来源缺一期），S02～S04 属于 1，S05 为 NaN，S06、S07 属于 2。
	rows := rankedRows(7, func(i int) map[string]float64 {
		switch {
		case i == 1:
			return nil
		case i <= 4:
			return map[string]float64{"daily.g": 1}
		case i == 5:
			return map[string]float64{"daily.g": math.NaN()}
		default:
			return map[string]float64{"daily.g": 2}
		}
	})
	decision := evaluate(t, program, frameOf(program, rows), State{})
	assertOK(t, decision)
	require.Equal(t, []string{"S02", "S03", "S06", "S07"}, weightedIDs(decision))
	require.Equal(t, "missing:daily.g", findItem(t, decision, "r", "S01").Reason)
	require.Equal(t, reasonGroupFull, findItem(t, decision, "r", "S04").Reason)
	require.Equal(t, reasonGroupNone, findItem(t, decision, "r", "S05").Reason)
}

// group.quotas 与 rest：按组内最优名次给组排序，第 i 组名额 quotas[i]，其余组 rest。
func TestSelectGroupQuotasAndRest(t *testing.T) {
	program := compile(t, selectRule("{top: 6, group: {by: g, quotas: [2, 1], rest: 1}}"), "m", "g")
	// 名次顺序的组：S01=A S02=A S03=A S04=B S05=B S06=C S07=C S08=D
	groups := []float64{0, 10, 10, 10, 20, 20, 30, 30, 40}
	rows := rankedRows(8, func(i int) map[string]float64 { return map[string]float64{"g": groups[i]} })
	decision := evaluate(t, program, frameOf(program, rows), State{})
	assertOK(t, decision)
	// A 组 2 个、B 组 1 个、C、D 组各 1 个（rest），只选出 5 个，不足 6 个时不越过名额。
	require.Equal(t, []string{"S01", "S02", "S04", "S06", "S08"}, weightedIDs(decision))
	require.Equal(t, reasonGroupFull, findItem(t, decision, "r", "S03").Reason)
	require.Equal(t, reasonGroupFull, findItem(t, decision, "r", "S07").Reason)
}

// buffer 保留的上期持仓先占所属组的名额。
func TestSelectBufferHeldOccupiesGroupSlot(t *testing.T) {
	program := compile(t, selectRule("{top: 2, buffer: 2, group: {by: g, max: 1}}"), "m", "g")
	// S01、S02 是组 1，S03、S04 是组 2；上期持有 S02（第 2 名，在缓冲区内）。
	rows := rankedRows(4, func(i int) map[string]float64 {
		if i <= 2 {
			return map[string]float64{"g": 1}
		}
		return map[string]float64{"g": 2}
	})
	previous := State{Rules: map[string]RuleState{"r": {Held: []string{"S02"}}}}
	decision := evaluate(t, program, frameOf(program, rows), previous)
	assertOK(t, decision)
	require.Equal(t, []string{"S02", "S03"}, weightedIDs(decision))
	require.Equal(t, reasonGroupFull, findItem(t, decision, "r", "S01").Reason)
}

// then：在第一段的结果上按新分数再取；与 holding 同用时只在建仓 bar 执行，延续批次不变。
func TestSelectThenWithHolding(t *testing.T) {
	program := compile(t, selectRule("{top: 4, then: {score: \"-v\", top: 2}}", "holding: {bars: 3, offsets: [0]}"), "m", "v")
	// 第一段选 S01～S04；v 依次 4、1、3、2，then 按 -v 取最高的两个：S02、S04。
	vs := []float64{0, 4, 1, 3, 2, 0, 0}
	rows := rankedRows(6, func(i int) map[string]float64 { return map[string]float64{"v": vs[i]} })
	frame := frameOf(program, rows)
	frame.BarIndex = 30
	first := evaluate(t, program, frame, State{})
	assertOK(t, first)
	require.Equal(t, []string{"S02", "S04"}, weightedIDs(first))
	require.Equal(t, reasonThen, findItem(t, first, "r", "S01").Reason)
	require.Equal(t, "not_selected", findItem(t, first, "r", "S05").Reason)
	// 非建仓 bar：分数完全改变，批次照旧。
	rows = rankedRows(6, func(i int) map[string]float64 { return map[string]float64{"v": float64(i)} })
	frame = frameOf(program, rows)
	frame.BarIndex = 31
	second := evaluate(t, program, frame, first.State)
	assertOK(t, second)
	require.Equal(t, []string{"S02", "S04"}, weightedIDs(second))
	// 下一个建仓 bar：v = i，-v 最高的是 S01、S02。
	frame.BarIndex = 33
	third := evaluate(t, program, frame, second.State)
	assertOK(t, third)
	require.Equal(t, []string{"S01", "S02"}, weightedIDs(third))
}

// then 写比例：个数 = 第一段选中且分数有效的个数 × 比例（默认向下取整），count_min 保证最少个数。
func TestSelectThenRatio(t *testing.T) {
	vs := []float64{0, 4, 1, 3, 2, 5, 0}
	rows := rankedRows(6, func(i int) map[string]float64 { return map[string]float64{"v": vs[i]} })
	// 第一段选 S01～S04，v 依次为 4、1、3、2；按 -v 取一半（4 × 0.5 = 2 个）。
	program := compile(t, selectRule("{top: 4, then: {score: \"-v\", top: 0.5}}"), "m", "v")
	decision := evaluate(t, program, frameOf(program, rows), State{})
	assertOK(t, decision)
	require.Equal(t, []string{"S02", "S04"}, weightedIDs(decision), "第一段 S01～S04 的 v 为 4、1、3、2，-v 最高的两个是 S02、S04")
	require.Equal(t, reasonThen, findItem(t, decision, "r", "S01").Reason)
	// 3 × 0.5 = 1.5：默认向下取 1 个；count_round: ceil 取 2 个；count_min: 2 把向下取整的 1 抬到 2。
	for spec, want := range map[string]int{
		"{top: 3, then: {score: \"-v\", top: 0.5}}":                    1,
		"{top: 3, then: {score: \"-v\", top: 0.5, count_round: ceil}}": 2,
		"{top: 3, then: {score: \"-v\", top: 0.5, count_min: 2}}":      2,
		"{top: 4, then: {score: \"-v\", bottom: 0.25}}":                1,
	} {
		decision = evaluate(t, compile(t, selectRule(spec), "m", "v"), frameOf(compile(t, selectRule(spec), "m", "v"), rows), State{})
		assertOK(t, decision)
		require.Len(t, weightedIDs(decision), want, spec)
	}
}

// then.score 可以用截面函数，在第一段选中的集合上计算。
func TestSelectThenCrossSectionOnChosen(t *testing.T) {
	program := compile(t, selectRule("{top: 3, then: {score: \"rank(v)\", bottom: 1}}"), "m", "v")
	vs := []float64{0, 5, 7, 6, 1}
	rows := rankedRows(4, func(i int) map[string]float64 { return map[string]float64{"v": vs[i]} })
	decision := evaluate(t, program, frameOf(program, rows), State{})
	assertOK(t, decision)
	// S04 的 v=1 最小，但没进第一段；在 S01～S03 里 rank(v) 最低的是 S01。
	require.Equal(t, []string{"S01"}, weightedIDs(decision))
}

// then.score 对第一段选出的标的全部无效（NaN）时，只让这条规则本期不出目标，不整期跳过。
func TestSelectThenAllInvalidOnlyEmptiesRule(t *testing.T) {
	program := compile(t, `name: s
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2, then: {score: "v", top: 1}}
    weight: 0.5
  - id: o
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.5
portfolio: {max_missing: 1}
`, "m", "v")
	rows := rankedRows(3, func(int) map[string]float64 { return map[string]float64{"v": math.NaN()} })
	decision := evaluate(t, program, frameOf(program, rows), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"S01": "0.5"})
	require.Equal(t, reasonThen+":score_invalid", findItem(t, decision, "r", "S01").Reason)
}

// 写比例、配了 cap 且 cap × 选中数 < total：摘要里注明分不完的预算留作现金；分得完时没有这条说明。
func TestSelectRatioCapShortNote(t *testing.T) {
	text := "name: s\nrules:\n  - {id: r, type: rank, score: m, select: {top: 0.1}, weight: {total: 1, cap: 0.3}}\nportfolio: {max_missing: 1}\n"
	program := compile(t, text, "m")
	decision := evaluate(t, program, frameOf(program, rankedRows(20, nil)), State{})
	assertOK(t, decision)
	assertDecimal(t, "cash", decision.Summary.Cash, "0.4")
	require.Contains(t, strings.Join(decision.Summary.Notes, "\n"), "分不完的预算留作现金")
	enough := evaluate(t, program, frameOf(program, rankedRows(40, nil)), State{})
	require.NotContains(t, strings.Join(enough.Summary.Notes, "\n"), "分不完的预算")
}

// 个数为 0、或 holding 的非建仓 bar（本根没有做选择）时不写“分不完”的说明。
func TestSelectRatioCapShortNoteOnlyWhenSelecting(t *testing.T) {
	program := compile(t, "name: s\nrules:\n  - {id: r, type: rank, score: m, select: {top: 0.1}, weight: {total: 1, cap: 0.3}}\nportfolio: {max_missing: 1}\n", "m")
	none := evaluate(t, program, frameOf(program, rankedRows(5, nil)), State{})
	require.NotContains(t, strings.Join(none.Summary.Notes, "\n"), "分不完的预算")
	holding := compile(t, "name: s\nrules:\n  - {id: r, type: rank, score: m, select: {top: 0.1}, weight: {total: 1, cap: 0.3}, holding: {bars: 3, offsets: [0]}}\nportfolio: {max_missing: 1}\n", "m")
	frame := frameOf(holding, rankedRows(20, nil))
	frame.BarIndex = 30
	built := evaluate(t, holding, frame, State{})
	require.Contains(t, strings.Join(built.Summary.Notes, "\n"), "分不完的预算")
	frame.BarIndex = 31
	carried := evaluate(t, holding, frame, built.State)
	require.NotContains(t, strings.Join(carried.Summary.Notes, "\n"), "分不完的预算")
}
