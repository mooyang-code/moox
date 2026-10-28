package engine

import (
	"math"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// 止损止盈（设计第 4 节）的判断与三种规则的处理。

func TestEvaluateStopLongAndShort(t *testing.T) {
	close := func(v float64) stopPrices { return stopPrices{close: v, adverse: v, favorable: v} }
	loss := &dsl.Stop{Loss: 0.05, Price: dsl.StopPriceClose}
	entry := newStopEntry(100, 1)
	require.Equal(t, reasonStopLoss, evaluateStop(loss, entry, close(95), false))
	require.Equal(t, "", evaluateStop(loss, entry, close(95.01), false))
	require.Equal(t, reasonStopLoss, evaluateStop(loss, entry, close(105), true))
	require.Equal(t, "", evaluateStop(loss, entry, close(104.99), true))

	profit := &dsl.Stop{Profit: 0.1, Price: dsl.StopPriceClose}
	require.Equal(t, reasonTakeProfit, evaluateStop(profit, entry, close(110), false))
	require.Equal(t, reasonTakeProfit, evaluateStop(profit, entry, close(90), true))

	// 极小阈值：价格不变不触发。
	tiny := &dsl.Stop{Loss: 1e-13, Price: dsl.StopPriceClose}
	require.Equal(t, "", evaluateStop(tiny, entry, close(100), false))
	require.Equal(t, reasonTakeProfit, strongerStopReason("", reasonTakeProfit))
	require.Equal(t, reasonStopLoss, strongerStopReason(reasonTakeProfit, reasonStopLoss))
	require.Equal(t, reasonStopLoss, strongerStopReason(reasonStopLoss, reasonTrailingStop))

	// 同期止损与止盈都满足（只可能在 extreme 下）：按止损记。
	both := &dsl.Stop{Loss: 0.05, Profit: 0.1, Price: dsl.StopPriceExtreme}
	require.Equal(t, reasonStopLoss, evaluateStop(both, entry, stopPrices{close: 100, adverse: 94, favorable: 111}, false))
}

// 设计 4.3 的示例：跟踪止损用此前的最有利价格判断，判断之后再用本根更新。
func TestTrailingStopUsesPreviousPeak(t *testing.T) {
	trailing := &dsl.Stop{Trailing: 0.08, Price: dsl.StopPriceExtreme}
	long := StopEntry{Price: "100", Peak: "110"}
	bar := stopPrices{close: 110, adverse: 102, favorable: 115}
	require.Equal(t, "", evaluateStop(trailing, long, bar, false)) // 止损线 101.2
	long = advancePeak(long, bar, false)
	require.Equal(t, "115", long.Peak) // 新止损线 105.8
	require.Equal(t, reasonTrailingStop, evaluateStop(trailing, long, stopPrices{close: 106, adverse: 105.8, favorable: 107}, false))

	short := StopEntry{Price: "100", Peak: "90"}
	bar = stopPrices{close: 90, adverse: 96, favorable: 85}
	require.Equal(t, "", evaluateStop(trailing, short, bar, true)) // 止损线 97.2
	require.Equal(t, "85", advancePeak(short, bar, true).Peak)
}

func TestReadStopPricesRejectsInvalid(t *testing.T) {
	_, ok := readStopPrices(Row{Values: map[string]float64{"close": 0}}, true, false, dsl.StopPriceClose)
	require.False(t, ok)
	_, ok = readStopPrices(Row{Values: map[string]float64{"close": math.NaN()}}, true, false, dsl.StopPriceClose)
	require.False(t, ok)
	_, ok = readStopPrices(Row{Values: map[string]float64{"close": 10}}, true, false, dsl.StopPriceExtreme)
	require.False(t, ok, "extreme 缺 high/low")
	prices, ok := readStopPrices(Row{Values: map[string]float64{"close": 10, "high": 12, "low": 9}}, true, true, dsl.StopPriceExtreme)
	require.True(t, ok)
	require.Equal(t, stopPrices{close: 10, adverse: 12, favorable: 9}, prices)
}

type stopRun struct {
	t       *testing.T
	program *dsl.Program
	state   State
	bar     int64
}

func newStopRun(t *testing.T, text string, columns ...string) *stopRun {
	strategy, err := dsl.Parse([]byte(text))
	require.NoError(t, err)
	program, err := dsl.Compile(strategy, columns)
	require.NoError(t, err)
	return &stopRun{t: t, program: program, bar: 100}
}

// step 用给定的行求值一期，返回目标权重与解释明细。
func (r *stopRun) step(rows map[string]map[string]float64) (map[string]string, map[string]Item, Decision) {
	r.t.Helper()
	ids := make([]string, 0, len(rows))
	frameRows := make(map[string]Row, len(rows))
	for id, values := range rows {
		ids = append(ids, id)
		frameRows[id] = Row{Values: values}
	}
	sortStringSlice(ids)
	expected := map[string][]string{}
	for _, rule := range r.program.Rules {
		expected[rule.Rule.ID] = ids
	}
	frame := Frame{BarIndex: r.bar, Rows: frameRows, Universe: ids, Expected: expected, AgedOut: map[string][]string{}}
	decision, err := Evaluate(r.program, frame, r.state)
	require.NoError(r.t, err)
	require.Equal(r.t, StatusOK, decision.Status, "%v", decision.Summary.Notes)
	r.state = decision.State
	r.bar++
	weights := map[string]string{}
	for _, target := range decision.Targets {
		weights[target.InstrumentID] = target.Weight.String()
	}
	items := map[string]Item{}
	for _, item := range decision.Items {
		_, dup := items[item.InstrumentID]
		require.False(r.t, dup, "同一标的有两条明细：%s", item.InstrumentID)
		items[item.InstrumentID] = item
	}
	return weights, items, decision
}

func sortStringSlice(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func scoreRow(close, m float64) map[string]float64 { return map[string]float64{"close": close, "m": m} }

// rank：止损后由后面的名次补位；冷却 [T, T+N] 闭区间内不能重新入选；明细与摘要。
func TestRankStopRefillsAndCooldown(t *testing.T) {
	run := newStopRun(t, `name: s
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
    stop: {loss: 0.1, cooldown_bars: 1}
portfolio: {max_missing: 1}
`, "close", "m")
	weights, _, _ := run.step(map[string]map[string]float64{"A-USDT": scoreRow(100, 9), "B-USDT": scoreRow(50, 5)})
	require.Equal(t, map[string]string{"A-USDT": "1"}, weights)
	require.Equal(t, "100", run.state.Rules["r"].Entries["A-USDT"].Price)

	// A 跌 10%：止损，B 补位；A 仍是第一名但不能入选。
	weights, items, decision := run.step(map[string]map[string]float64{"A-USDT": scoreRow(90, 9), "B-USDT": scoreRow(50, 5)})
	require.Equal(t, map[string]string{"B-USDT": "1"}, weights)
	require.Equal(t, StageStopped, items["A-USDT"].Stage)
	require.Equal(t, reasonStopLoss, items["A-USDT"].Reason)
	require.Equal(t, 1, decision.Summary.Rules["r"].StopLoss)
	require.Equal(t, run.bar-1+1, run.state.Rules["r"].Cooldown["A-USDT"]) // 截止序号 = T + 1

	// T+1 仍在冷却：A 记 cooldown。
	weights, items, _ = run.step(map[string]map[string]float64{"A-USDT": scoreRow(90, 9), "B-USDT": scoreRow(50, 5)})
	require.Equal(t, map[string]string{"B-USDT": "1"}, weights)
	require.Equal(t, reasonCooldown, items["A-USDT"].Reason)

	// T+2 冷却结束，A 重新入选，入场价是本期 close。
	weights, _, _ = run.step(map[string]map[string]float64{"A-USDT": scoreRow(80, 9), "B-USDT": scoreRow(50, 5)})
	require.Equal(t, map[string]string{"A-USDT": "1"}, weights)
	require.Equal(t, "80", run.state.Rules["r"].Entries["A-USDT"].Price)
}

// cooldown_bars: 0 只禁止触发当期；本期没有参考价时不判止损。
func TestRankStopZeroCooldownAndMissingPrice(t *testing.T) {
	run := newStopRun(t, `name: s
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
    stop: {loss: 0.1}
portfolio: {max_missing: 1}
`, "close", "m")
	run.step(map[string]map[string]float64{"A-USDT": scoreRow(100, 9), "B-USDT": scoreRow(50, 5)})
	// close 缺失（score 只用 m，A 仍可用）：不判止损，A 继续持有。
	weights, _, _ := run.step(map[string]map[string]float64{"A-USDT": {"m": 9}, "B-USDT": scoreRow(50, 5)})
	require.Equal(t, map[string]string{"A-USDT": "1"}, weights)
	weights, _, _ = run.step(map[string]map[string]float64{"A-USDT": scoreRow(80, 9), "B-USDT": scoreRow(50, 5)})
	require.Equal(t, map[string]string{"B-USDT": "1"}, weights)
	weights, _, _ = run.step(map[string]map[string]float64{"A-USDT": scoreRow(80, 9), "B-USDT": scoreRow(50, 5)})
	require.Equal(t, map[string]string{"A-USDT": "1"}, weights, "冷却 0 根：下一期即可重新入选")
}

// rank holding：止损的批次成员移出，份额留现金；入场价取实际进入批次的 close。
func TestRankHoldingStopLeavesCash(t *testing.T) {
	run := newStopRun(t, `name: s
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2}
    weight: {total: 1}
    holding: {bars: 4, offsets: [0]}
    stop: {loss: 0.1, cooldown_bars: 10}
portfolio: {max_missing: 1}
`, "close", "m")
	run.bar = 101 // 首次建立：建仓序号回填到 100，入场价仍取本期 close
	weights, _, _ := run.step(map[string]map[string]float64{"A-USDT": scoreRow(100, 9), "B-USDT": scoreRow(50, 5), "C-USDT": scoreRow(20, 1)})
	require.Equal(t, map[string]string{"A-USDT": "0.5", "B-USDT": "0.5"}, weights)
	batch := run.state.Rules["r"].Batches[0]
	require.Equal(t, int64(100), batch.EstablishedBar)
	require.Equal(t, StopEntry{Price: "100", Bar: 101, Peak: "100"}, batch.Entries["A-USDT"])
	weights, items, _ := run.step(map[string]map[string]float64{"A-USDT": scoreRow(85, 9), "B-USDT": scoreRow(50, 5), "C-USDT": scoreRow(20, 1)})
	require.Equal(t, map[string]string{"B-USDT": "0.5"}, weights)
	require.Equal(t, StageStopped, items["A-USDT"].Stage)
}

// signal：止损等同 exit，exit 的输入缺失也照常判断；明细合并成一条；做空方向。
func TestSignalStopShortAndMissingExitInput(t *testing.T) {
	run := newStopRun(t, `name: s
rules:
  - id: r
    type: signal
    pool: [A-USDT]
    side: short
    entry: "e > 0"
    exit: "x > 0"
    weight: {total: 1}
    stop: {loss: 0.05, cooldown_bars: 3}
portfolio: {max_missing: 1}
`, "close", "e", "x")
	weights, _, _ := run.step(map[string]map[string]float64{"A-USDT": {"close": 100, "e": 1, "x": 0}})
	require.Equal(t, map[string]string{"A-USDT": "-1"}, weights)
	// 做空：价格涨 5% 触发；x 缺失（本来按缺数清仓），明细合并为一条 stopped。
	weights, items, _ := run.step(map[string]map[string]float64{"A-USDT": {"close": 105, "e": 1}})
	require.Empty(t, weights)
	require.Equal(t, StageStopped, items["A-USDT"].Stage)
	require.Contains(t, items["A-USDT"].Reason, reasonStopLoss+";missing:x")
	// 冷却中 entry 成立也不入场。
	weights, items, _ = run.step(map[string]map[string]float64{"A-USDT": {"close": 105, "e": 1, "x": 0}})
	require.Empty(t, weights)
	require.Equal(t, reasonCooldown, items["A-USDT"].Reason)
}

// tranche：同组子策略按各自阈值判断，共用入场价与最有利价格；冷却中的子策略不为新开仓取权重；止盈。
func TestTrancheStopPerSub(t *testing.T) {
	run := newStopRun(t, `name: s
rules:
  - id: g
    type: tranche
    weight: 1
    tranche:
      hold_period: 3
      long_factors: [{factor: m, descending: true}]
      stg_weight: 1
      stop: {loss: "{l}", profit: 0.5, cooldown_bars: 5}
    params:
      columns: [l]
      rows:
        - [0.05]
        - [0.2]
portfolio: {leverage: 10, max_missing: 1}
`, "close", "volume", "m")
	require.Len(t, run.program.Rules[0].TrancheGroups, 1, "阈值不同、价格口径相同的子策略同组")
	row := func(close, m float64) map[string]float64 {
		return map[string]float64{"close": close, "volume": 1, "m": m}
	}
	// 每个子策略每批份额 1/3，两个子策略 stg_weight 各半。
	weights, _, _ := run.step(map[string]map[string]float64{"A-USDT": row(100, 9), "B-USDT": row(100, 1)})
	require.Equal(t, map[string]string{"A-USDT": "0.3333"}, weights)
	// A 跌 10%：第 1 个子策略（5%）止损、第 2 个（20%）不止损。A 仍有权重，原因追加 stopped:1。
	weights, items, decision := run.step(map[string]map[string]float64{"A-USDT": row(90, 9), "B-USDT": row(100, 1)})
	require.Equal(t, 1, decision.Summary.Rules["g"].StopLoss)
	require.Contains(t, items["A-USDT"].Reason, "stopped:1")
	// 第 1 批：只剩子策略 2 的 1/6；第 2 批（本期新开，子策略 1 在冷却中）：只有子策略 2 的 1/6。
	require.Equal(t, "0.3333", weights["A-USDT"])
	pick := run.state.Rules["g"].Tranche.Groups[0].Picks[0]
	require.True(t, passBit(pick.Stopped, 0))
	require.False(t, passBit(pick.Stopped, 1))
	require.Equal(t, "100", pick.Peak)
	// 止盈：第 1 批相对 100 涨 50%，子策略 2 止盈；第 2 批入场 90，涨 66%，也止盈。
	_, _, decision = run.step(map[string]map[string]float64{"A-USDT": row(150, 1), "B-USDT": row(100, 9)})
	require.Equal(t, 2, decision.Summary.Rules["g"].TakeProfit)
}

// 止损用的列是可选列：编译时要求 View 有，但不进规则的必需列；extreme 要 high/low。
func TestStopColumns(t *testing.T) {
	text := "name: s\nrules:\n  - id: r\n    type: rank\n    score: \"m\"\n    select: {top: 1}\n    weight: {total: 1}\n    stop: {loss: 0.1, price: extreme}\n"
	strategy, err := dsl.Parse([]byte(text))
	require.NoError(t, err)
	_, err = dsl.Compile(strategy, []string{"close", "m"})
	require.ErrorContains(t, err, "high")
	program, err := dsl.Compile(strategy, []string{"close", "high", "low", "m"})
	require.NoError(t, err)
	require.Equal(t, []string{"m"}, program.Rules[0].Columns())
	require.ElementsMatch(t, []string{"close", "high", "low", "m"}, program.Columns)
}
