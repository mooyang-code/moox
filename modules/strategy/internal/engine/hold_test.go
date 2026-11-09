package engine

import (
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// holdRun 逐期驱动一条规则：每期给出各标的的列值（previous 给出上一根的列值），返回目标、明细与决策。
type holdRun struct {
	t       *testing.T
	program *dsl.Program
	state   State
	bar     int64
	clock   *HoldingClock
}

func newHoldRun(t *testing.T, text string, columns ...string) *holdRun {
	t.Helper()
	return &holdRun{t: t, program: compile(t, text, columns...), bar: 100}
}

func (r *holdRun) step(rows map[string]map[string]float64) (map[string]string, map[string]Item, Decision) {
	return r.stepWith(rows, nil)
}

func (r *holdRun) stepWith(rows map[string]map[string]float64, previous map[string]map[string]float64) (map[string]string, map[string]Item, Decision) {
	r.t.Helper()
	ids := make([]string, 0, len(rows))
	frameRows := make(map[string]Row, len(rows))
	for id, values := range rows {
		ids = append(ids, id)
		frameRows[id] = Row{Values: values, Previous: previous[id]}
	}
	sort.Strings(ids)
	expected := map[string][]string{}
	for _, rule := range r.program.Rules {
		expected[rule.Rule.ID] = ids
	}
	frame := Frame{BarIndex: r.bar, Rows: frameRows, Universe: ids, Expected: expected, AgedOut: map[string][]string{}}
	if r.clock != nil {
		frame.Holdings = map[string]HoldingClock{"r": *r.clock}
	}
	decision, err := evalFrame(r.program, frame, r.state)
	require.NoError(r.t, err)
	require.Equal(r.t, StatusOK, decision.Status, "%v", decision.Summary.Notes)
	assertUniqueItems(r.t, decision)
	r.state = decision.State
	r.bar++
	items := map[string]Item{}
	for _, item := range decision.Items {
		items[item.InstrumentID] = item
	}
	return weightsOf(decision), items, decision
}

func row(pairs ...float64) map[string]float64 {
	names := []string{"m", "f", "x", "close"}
	out := map[string]float64{}
	for i, value := range pairs {
		out[names[i]] = value
	}
	return out
}

const holdBase = `name: h
rules:
  - id: r
    type: rank
    filter: "f > 0"
    score: "m"
    select: {top: 2}
    hold: {entry: "x < 5", exit: "x > 8", max_bars: 3, cooldown_bars: COOLDOWN}
    weight: {total: 1}
portfolio: {max_missing: 1}
`

func holdProgram(cooldown string) string {
	return strings.ReplaceAll(holdBase, "COOLDOWN", cooldown)
}

// 入场、保持与各种出场：exit 为真、不再满足 filter、持满 max_bars；冷却为 0 时下一根即可再入场。
func TestHoldEntryExitReasons(t *testing.T) {
	run := newHoldRun(t, holdProgram("0"), "m", "f", "x")
	// 第 1 根：A、B 分数最高且 entry 为真；C 的 x=6 不满足 entry。
	weights, items, _ := run.step(map[string]map[string]float64{"A": row(9, 1, 1), "B": row(8, 1, 1), "C": row(10, 1, 6), "D": row(1, 1, 1)})
	require.Equal(t, map[string]string{"A": "0.5", "B": "0.5"}, weights)
	require.Equal(t, reasonHoldEntered, items["A"].Reason)
	require.Equal(t, reasonHoldNotEntered, items["C"].Reason)
	require.Equal(t, StageIdle, items["C"].Stage)
	// 第 2 根：A 的 x>8 出场；B 不再满足 entry 但仍持有（entry 只管入场）；C 入场（x 回到 1）。
	weights, items, _ = run.step(map[string]map[string]float64{"A": row(9, 1, 9), "B": row(8, 1, 7), "C": row(10, 1, 1), "D": row(1, 1, 1)})
	require.Equal(t, map[string]string{"B": "0.5", "C": "0.5"}, weights)
	require.Equal(t, reasonExitCondition, items["A"].Reason)
	require.Equal(t, reasonHoldKept, items["B"].Reason)
	// 第 3 根：B 不再满足 filter 出场；冷却为 0，A 本根可以再入场。
	weights, items, _ = run.step(map[string]map[string]float64{"A": row(9, 1, 1), "B": row(8, 0, 1), "C": row(10, 1, 1), "D": row(1, 1, 1)})
	require.Equal(t, map[string]string{"A": "0.5", "C": "0.5"}, weights)
	require.Equal(t, reasonExitFilter, items["B"].Reason)
	// 第 4、5 根：C 在第 2 根（101）入场，104 时持满 3 根出场，由 B 补位。
	run.step(map[string]map[string]float64{"A": row(9, 1, 1), "B": row(8, 1, 1), "C": row(10, 1, 1), "D": row(1, 1, 1)})
	weights, items, _ = run.step(map[string]map[string]float64{"A": row(9, 1, 1), "B": row(8, 1, 1), "C": row(10, 1, 1), "D": row(1, 1, 1)})
	require.Equal(t, reasonExitMaxBars, items["C"].Reason)
	require.Equal(t, map[string]string{"A": "0.5", "B": "0.5"}, weights)
}

// 冷却大于 0：出场后 cooldown_bars 根内不能再入场，到期后可以。
func TestHoldCooldown(t *testing.T) {
	run := newHoldRun(t, holdProgram("2"), "m", "f", "x")
	rows := func(ax float64) map[string]map[string]float64 {
		return map[string]map[string]float64{"A": row(9, 1, ax), "B": row(8, 1, 1), "C": row(1, 1, 1)}
	}
	run.step(rows(1))
	_, items, _ := run.step(rows(9)) // A 在 101 出场，冷却到 103（含）
	require.Equal(t, reasonExitCondition, items["A"].Reason)
	weights, items, _ := run.step(rows(1))
	require.Equal(t, reasonHoldCooling, items["A"].Reason)
	require.Equal(t, map[string]string{"B": "0.5", "C": "0.5"}, weights)
	_, items, _ = run.step(rows(1))
	require.Equal(t, reasonHoldCooling, items["A"].Reason)
	// 104：冷却结束；C 自 101 入场持满 3 根出场、A 补位（B 已在 103 持满出场，两者都在冷却）。
	_, items, _ = run.step(rows(1))
	require.Equal(t, reasonHoldEntered, items["A"].Reason)
	require.Equal(t, reasonExitMaxBars, items["C"].Reason)
	require.Equal(t, map[string]int64{"B": 105, "C": 106}, nonNilMap(run.state.Rules["r"].Hold.Cooldown))
}

func nonNilMap(values map[string]int64) map[string]int64 {
	if values == nil {
		return map[string]int64{}
	}
	return values
}

// 同一根 entry 与 exit 都为真时不入场。
func TestHoldEntryAndExitSameBar(t *testing.T) {
	run := newHoldRun(t, `name: h
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    hold: {entry: "x > 0", exit: "x > 5"}
    weight: 1
portfolio: {max_missing: 1}
`, "m", "x")
	weights, items, _ := run.step(map[string]map[string]float64{"A": {"m": 2, "x": 6}, "B": {"m": 1, "x": 1}})
	require.Equal(t, map[string]string{"B": "1"}, weights)
	require.Equal(t, reasonHoldNotEntered, items["A"].Reason)
}

// 持有标的只有 score 缺值时不出场；filter 引用的列缺值时按缺数出场（exit_missing）。
func TestHoldMissingScoreKeepsAndMissingFilterExits(t *testing.T) {
	run := newHoldRun(t, holdProgram("0"), "m", "f", "x")
	run.step(map[string]map[string]float64{"A": row(9, 1, 1), "B": row(8, 1, 1), "C": row(1, 1, 1)})
	weights, items, _ := run.step(map[string]map[string]float64{"A": {"f": 1, "x": 1}, "B": {"m": 8, "x": 1}, "C": row(1, 1, 1)})
	require.Equal(t, map[string]string{"A": "0.5", "C": "0.5"}, weights)
	require.Equal(t, reasonHoldKept, items["A"].Reason)
	require.Equal(t, StageMissing, items["B"].Stage)
	require.Equal(t, reasonExitMissing+";missing:f", items["B"].Reason)
}

// 止损与 exit 同根成立时按止损记，冷却按 stop.cooldown_bars。
func TestHoldStopBeforeExit(t *testing.T) {
	run := newHoldRun(t, `name: h
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    hold: {exit: "x > 8", cooldown_bars: 0}
    stop: {loss: 0.1, cooldown_bars: 3}
    weight: 1
portfolio: {max_missing: 1}
`, "m", "x", "close")
	run.step(map[string]map[string]float64{"A": {"m": 2, "x": 1, "close": 100}, "B": {"m": 1, "x": 1, "close": 100}})
	weights, items, decision := run.step(map[string]map[string]float64{"A": {"m": 2, "x": 9, "close": 80}, "B": {"m": 1, "x": 1, "close": 100}})
	require.Equal(t, map[string]string{"B": "1"}, weights)
	require.Equal(t, StageStopped, items["A"].Stage)
	require.Equal(t, reasonExitStop+":"+reasonStopLoss, items["A"].Reason)
	require.Equal(t, 1, decision.Summary.Rules["r"].StopLoss)
	require.Equal(t, int64(104), run.state.Rules["r"].Cooldown["A"])
}

// 冻结的持仓（只缺上一根）不判出场、占用名额，份额由组合层保持。
func TestHoldFrozenOccupiesSlot(t *testing.T) {
	run := newHoldRun(t, `name: h
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2}
    hold: {exit: "bars[-1].x > 8"}
    weight: 1
portfolio: {max_missing: 1}
`, "m", "x")
	prev := map[string]map[string]float64{"A": {"x": 1}, "B": {"x": 1}, "C": {"x": 1}}
	run.stepWith(map[string]map[string]float64{"A": {"m": 3, "x": 1}, "B": {"m": 2, "x": 1}, "C": {"m": 1, "x": 1}}, prev)
	// A 只缺上一根：冻结，B 继续持有，C 不能入场（名额已满）。
	weights, items, decision := run.stepWith(map[string]map[string]float64{"A": {"m": 3, "x": 1}, "B": {"m": 2, "x": 1}, "C": {"m": 9, "x": 1}}, map[string]map[string]float64{"B": {"x": 1}, "C": {"x": 1}})
	require.Equal(t, StageHeld, items["A"].Stage)
	require.Equal(t, reasonNotSelected, items["C"].Reason)
	require.Equal(t, "0.5", weights["B"])
	require.Equal(t, "0.5", weights["A"]) // 组合层保持上期目标
	require.True(t, holdsTarget(decision, "A"))
	require.Equal(t, []string{"A", "B"}, run.state.Rules["r"].Held)
}

func holdsTarget(decision Decision, id string) bool {
	for _, target := range decision.Targets {
		if target.InstrumentID == id {
			return target.Hold
		}
	}
	return false
}

// 目标数按比例：样本变少时目标数变小，持有数超过目标不裁减；持有数少于目标时预算按实际持有数分满。
func TestHoldRatioTargetMoreAndFewer(t *testing.T) {
	run := newHoldRun(t, `name: h
rules:
  - id: r
    type: rank
    filter: "f > 0"
    score: "m"
    select: {top: 0.5}
    hold: {entry: "x < 5"}
    weight: 1
portfolio: {max_missing: 1}
`, "m", "f", "x")
	all := map[string]map[string]float64{"A": row(4, 1, 1), "B": row(3, 1, 1), "C": row(2, 1, 1), "D": row(1, 1, 1)}
	weights, _, _ := run.step(all) // 4 × 0.5 = 2
	require.Equal(t, map[string]string{"A": "0.5", "B": "0.5"}, weights)
	// C、D 不再通过 filter，样本只剩 2 个、目标 1 个；A、B 都留下，不裁减。
	weights, _, _ = run.step(map[string]map[string]float64{"A": row(4, 1, 1), "B": row(3, 1, 1), "C": row(2, 0, 1), "D": row(1, 0, 1)})
	require.Equal(t, map[string]string{"A": "0.5", "B": "0.5"}, weights)
	// 目标回到 2、只有 A 满足 entry 且 B 已出场：持有 1 个，预算全部给它。
	weights, _, _ = run.step(map[string]map[string]float64{"A": row(4, 1, 1), "B": row(3, 0, 1), "C": row(2, 1, 6), "D": row(1, 1, 6)})
	require.Equal(t, map[string]string{"A": "1"}, weights)
}

// 分组名额：持有标的先占组名额，补位跳过已满的组。
func TestHoldGroupQuota(t *testing.T) {
	run := newHoldRun(t, `name: h
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2, group: {by: g, max: 1}}
    hold: {}
    weight: 1
portfolio: {max_missing: 1}
`, "m", "g")
	weights, _, _ := run.step(map[string]map[string]float64{"A": {"m": 3, "g": 1}, "B": {"m": 2, "g": 1}, "C": {"m": 1, "g": 2}})
	require.Equal(t, map[string]string{"A": "0.5", "C": "0.5"}, weights)
	// A 分数掉到最后仍持有，占着组 1：B 不能入场。
	_, items, _ := run.step(map[string]map[string]float64{"A": {"m": 0, "g": 1}, "B": {"m": 5, "g": 1}, "C": {"m": 1, "g": 2}})
	require.Equal(t, reasonNotSelected, items["B"].Reason)
}

// 与日历同写：只在建仓 bar 判 filter、exit、max_bars 与补位，其余 bar 保持；漏掉建仓 bar 时在之后第一根 ok bar 补判一次；
// 停用再启用后状态清零。
func TestHoldWithCalendar(t *testing.T) {
	run := newHoldRun(t, `name: h
rules:
  - id: r
    type: rank
    filter: "f > 0"
    score: "m"
    select: {top: 1}
    holding: {calendar: week, days: [1]}
    hold: {exit: "x > 8", max_bars: 2}
    weight: 1
portfolio: {max_missing: 1}
`, "m", "f", "x")
	run.clock = &HoldingClock{Scheduled: map[int]int64{1: 98}}
	weights, _, _ := run.step(map[string]map[string]float64{"A": row(2, 1, 1), "B": row(1, 1, 1)}) // 100：首次求值
	require.Equal(t, map[string]string{"A": "1"}, weights)
	require.Equal(t, int64(98), run.state.Rules["r"].Hold.Decided)
	// 101：不是建仓 bar，A 的 exit 为真、filter 失败都不出场；B 分数更高也不换。
	weights, items, _ := run.step(map[string]map[string]float64{"A": row(2, 0, 9), "B": row(5, 1, 1)})
	require.Equal(t, map[string]string{"A": "1"}, weights)
	require.Equal(t, reasonNotRebalanced, items["B"].Reason)
	// 建仓 bar 102 没有 ok 决策，103 上补判一次：A 出场，B 入场。
	run.bar = 103
	run.clock = &HoldingClock{Scheduled: map[int]int64{1: 102}}
	weights, items, _ = run.step(map[string]map[string]float64{"A": row(2, 0, 9), "B": row(5, 1, 1)})
	require.Equal(t, map[string]string{"B": "1"}, weights)
	require.Equal(t, reasonExitFilter, items["A"].Reason)
	require.Equal(t, int64(102), run.state.Rules["r"].Hold.Decided)
	// 104：同一个建仓日已判过，不再判。
	_, items, _ = run.step(map[string]map[string]float64{"A": row(9, 1, 1), "B": row(5, 1, 1)})
	require.Equal(t, reasonNotRebalanced, items["A"].Reason)
	// 106 是下一个建仓 bar：B 自 103 入场持有 3 根 ≥ max_bars，出场后 A 入场。
	run.bar = 106
	run.clock = &HoldingClock{Scheduled: map[int]int64{1: 106}}
	weights, items, _ = run.step(map[string]map[string]float64{"A": row(9, 1, 1), "B": row(5, 1, 1)})
	require.Equal(t, reasonExitMaxBars, items["B"].Reason)
	require.Equal(t, map[string]string{"A": "1"}, weights)
	// 停用再启用：前序状态为空，本根就判（即使不是建仓 bar），状态从零开始。
	run.state = State{}
	run.clock = &HoldingClock{Scheduled: map[int]int64{1: 106}}
	weights, _, _ = run.step(map[string]map[string]float64{"A": row(1, 1, 1), "B": row(5, 1, 1)})
	require.Equal(t, map[string]string{"B": "1"}, weights)
	require.Equal(t, map[string]int64{"B": 107}, run.state.Rules["r"].Hold.Entered)
}

// 停用再启用：hold 的入场序号与冷却清零，max_bars 从新的入场重新计数。
func TestHoldResetAfterReenable(t *testing.T) {
	run := newHoldRun(t, holdProgram("5"), "m", "f", "x")
	rows := map[string]map[string]float64{"A": row(9, 1, 1), "B": row(8, 1, 1), "C": row(1, 1, 1)}
	run.step(rows)
	run.step(map[string]map[string]float64{"A": row(9, 1, 9), "B": row(8, 1, 1), "C": row(1, 1, 1)})
	require.Contains(t, run.state.Rules["r"].Hold.Cooldown, "A")
	run.state = State{}
	weights, _, _ := run.step(rows)
	require.Equal(t, map[string]string{"A": "0.5", "B": "0.5"}, weights)
	require.Empty(t, run.state.Rules["r"].Hold.Cooldown)
	require.Equal(t, map[string]int64{"A": 102, "B": 102}, run.state.Rules["r"].Hold.Entered)
}

// 与日历同写：非建仓 bar 沿用上次判定时的基准权重。rank 配权下分数互换不翻转权重；持有标的缺数出场时，空出的份额留现金，
// 剩下的不重新分满；下一个建仓 bar 再按持有集合重新配权。
func TestHoldCalendarKeepsBaseWeights(t *testing.T) {
	run := newHoldRun(t, `name: h
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2}
    holding: {calendar: week, days: [1]}
    hold: {}
    weight: {total: 0.9, method: rank}
portfolio: {max_missing: 1}
`, "m", "f")
	run.clock = &HoldingClock{Scheduled: map[int]int64{1: 100}}
	weights, _, _ := run.step(map[string]map[string]float64{"A": {"m": 3, "f": 1}, "B": {"m": 2, "f": 1}, "C": {"m": 1, "f": 1}})
	require.Equal(t, map[string]string{"A": "0.6", "B": "0.3"}, weights)
	// 101：分数互换，权重不翻转。
	weights, _, _ = run.step(map[string]map[string]float64{"A": {"m": 1, "f": 1}, "B": {"m": 3, "f": 1}, "C": {"m": 2, "f": 1}})
	require.Equal(t, map[string]string{"A": "0.6", "B": "0.3"}, weights)
	// 102：A 缺数出场，B 仍是 0.3，A 的份额留现金，C 不补位。
	weights, items, decision := run.step(map[string]map[string]float64{"B": {"m": 3, "f": 1}, "C": {"m": 2, "f": 1}})
	require.Equal(t, map[string]string{"B": "0.3"}, weights)
	require.Equal(t, reasonNotRebalanced, items["C"].Reason)
	assertDecimal(t, "cash", decision.Summary.Cash, "0.7")
	// 107：下一个建仓 bar，B、C 按名次重新配权。
	run.bar = 107
	run.clock = &HoldingClock{Scheduled: map[int]int64{1: 107}}
	weights, _, _ = run.step(map[string]map[string]float64{"B": {"m": 3, "f": 1}, "C": {"m": 2, "f": 1}})
	require.Equal(t, map[string]string{"B": "0.6", "C": "0.3"}, weights)
}

// 分组值的回退只给冻结标的用：持有标的本根有行、group.by 为 NaN，即使上期记下了分组值，也按 group_none 处理（不占名额、不能留作分组）。
func TestHoldGroupFallbackOnlyForFrozen(t *testing.T) {
	run := newHoldRun(t, `name: h
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2, group: {by: g, max: 1}}
    hold: {}
    weight: 1
portfolio: {max_missing: 1}
`, "m", "g")
	run.state = State{Rules: map[string]RuleState{"r": {Held: []string{"A"}, Hold: &HoldState{Entered: map[string]int64{"A": 99}, Groups: map[string]float64{"A": 1}}}}, Targets: map[string]string{"A|" + testMarket: "1"}}
	weights, items, decision := run.step(map[string]map[string]float64{"A": {"m": 1, "g": math.NaN()}, "C": {"m": 9, "g": 1}})
	// A 的分组值是 NaN、不按上期的 1 占名额：同组的 C 可以入场；A 继续持有（hold 只看 filter、exit 等）。
	require.Equal(t, reasonHoldEntered, items["C"].Reason)
	require.Equal(t, map[string]string{"A": "0.5", "C": "0.5"}, weights)
	require.NotContains(t, decision.State.Rules["r"].Hold.Groups, "A")
}
