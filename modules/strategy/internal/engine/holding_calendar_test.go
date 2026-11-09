package engine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func holdingStep(t *testing.T, run *State, program string, bar int64, clock HoldingClock, scores map[string]float64) Decision {
	t.Helper()
	compiled := compile(t, program, "m")
	rows := make(map[string]Row, len(scores))
	for id, score := range scores {
		rows[id] = values("m", score)
	}
	frame := frameOf(compiled, rows)
	frame.BarIndex = bar
	frame.Holdings = map[string]HoldingClock{"r": clock}
	decision := evaluate(t, compiled, frame, *run)
	assertOK(t, decision)
	*run = decision.State
	return decision
}

func batchEstablished(state State) map[int]int64 {
	out := map[int]int64{}
	for _, batch := range state.Rules["r"].Batches {
		out[batch.Offset] = batch.EstablishedBar
	}
	return out
}

const weeklyHolding = `name: w
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
    holding: {calendar: week, days: [1, 3]}
portfolio: {max_missing: 1}
`

// 日历写法：首次求值建好全部批次，建仓序号取最近一个应建仓的 bar；建仓 bar 上重建；漏掉的建仓 bar 在之后第一根 ok bar 补建；
// 停用再启用（前序状态为空）从零开始。
func TestCalendarHoldingRebuildAndCatchUp(t *testing.T) {
	var state State
	a, b := map[string]float64{"A": 2, "B": 1}, map[string]float64{"A": 1, "B": 2}
	first := holdingStep(t, &state, weeklyHolding, 10, HoldingClock{Scheduled: map[int]int64{1: 7, 3: 9}}, a)
	require.Equal(t, map[string]string{"A": "1"}, weightsOf(first))
	require.Equal(t, map[int]int64{1: 7, 3: 9}, batchEstablished(state))
	// 非建仓 bar：分数反转，批次不动。
	second := holdingStep(t, &state, weeklyHolding, 11, HoldingClock{Scheduled: map[int]int64{1: 7, 3: 9}}, b)
	require.Equal(t, map[string]string{"A": "1"}, weightsOf(second))
	// 第 1 个交易日的建仓 bar：批次 1 用新名次重建，批次 3 延续。
	third := holdingStep(t, &state, weeklyHolding, 14, HoldingClock{Scheduled: map[int]int64{1: 14, 3: 9}}, b)
	require.Equal(t, map[string]string{"A": "0.5", "B": "0.5"}, weightsOf(third))
	require.Equal(t, map[int]int64{1: 14, 3: 9}, batchEstablished(state))
	// 批次 3 的建仓 bar 16 没有 ok 决策（这里直接跳过不求值），17 上补建，建仓序号记 16。
	fourth := holdingStep(t, &state, weeklyHolding, 17, HoldingClock{Scheduled: map[int]int64{1: 14, 3: 16}}, b)
	require.Equal(t, map[string]string{"B": "1"}, weightsOf(fourth))
	require.Equal(t, map[int]int64{1: 14, 3: 16}, batchEstablished(state))
	fifth := holdingStep(t, &state, weeklyHolding, 18, HoldingClock{Scheduled: map[int]int64{1: 14, 3: 16}}, a)
	require.Equal(t, map[string]string{"B": "1"}, weightsOf(fifth))
	// 停用再启用：前序状态为空，按本期名次重建全部批次。
	state = State{}
	again := holdingStep(t, &state, weeklyHolding, 19, HoldingClock{Scheduled: map[int]int64{1: 14, 3: 16}}, a)
	require.Equal(t, map[string]string{"A": "1"}, weightsOf(again))
	require.Equal(t, map[int]int64{1: 14, 3: 16}, batchEstablished(state))
	// 还没有过应建仓 bar 的批次：首次建立时建仓序号记本期，到它第一个建仓 bar 再重建。
	state = State{}
	holdingStep(t, &state, weeklyHolding, 20, HoldingClock{Scheduled: map[int]int64{1: 14}}, a)
	require.Equal(t, map[int]int64{1: 14, 3: 20}, batchEstablished(state))
	holdingStep(t, &state, weeklyHolding, 23, HoldingClock{Scheduled: map[int]int64{1: 14, 3: 23}}, b)
	require.Equal(t, map[int]int64{1: 14, 3: 23}, batchEstablished(state))
}

// 锚点只决定相位：bars 4、offset 0、锚点序号 2 时在 6、10 重建，建仓序号按锚点对齐；锚点晚于当前也一样。
func TestHoldingAnchorShiftsPhase(t *testing.T) {
	program := `name: a
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
    holding: {bars: 4, offsets: [0], anchor: "2024-01-01T00:00:00Z"}
portfolio: {max_missing: 1}
`
	for _, anchor := range []int64{2, 102} {
		var state State
		a, b := map[string]float64{"A": 2, "B": 1}, map[string]float64{"A": 1, "B": 2}
		holdingStep(t, &state, program, 5, HoldingClock{Anchor: anchor}, a)
		require.Equal(t, map[int]int64{0: 2}, batchEstablished(state))
		// 8 不是建仓 bar（没有锚点时 8 才是）。
		holdingStep(t, &state, program, 6, HoldingClock{Anchor: anchor}, b)
		require.Equal(t, map[int]int64{0: 6}, batchEstablished(state))
		require.Equal(t, map[string]string{"B": "1"}, weightsOf(holdingStep(t, &state, program, 8, HoldingClock{Anchor: anchor}, a)))
		require.Equal(t, map[string]string{"A": "1"}, weightsOf(holdingStep(t, &state, program, 10, HoldingClock{Anchor: anchor}, a)))
	}
}
