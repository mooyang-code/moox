package trigger

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

const holdCatchupDSL = `name: hold_demo
bar: 1h
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    hold: {exit: "m < 2", max_bars: 2, cooldown_bars: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

func newHoldHarness(t *testing.T) *harness {
	t.Helper()
	resolved := input.Resolved{Market: dsl.SingleMarket("spot"), Tasks: map[string]input.TaskBinding{"spot": {ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds"}}, Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: "f", FactorID: "f", DefinitionHash: "h1"}}, Factors: map[string]string{"f": "h1|fixed"}, ViewColumns: []string{"close", "volume", "m"}}
	h := newHarnessWith(t, holdCatchupDSL, nil, resolved)
	// 每期分数轮换：入场序号、max_bars 出场与冷却都依赖之前每一期的状态。
	scores := []map[string]float64{
		{"A": 9, "B": 5, "C": 3},
		{"A": 9, "B": 5, "C": 3},
		{"A": 9, "B": 5, "C": 3},
		{"A": 1, "B": 5, "C": 3},
		{"A": 9, "B": 5, "C": 3},
	}
	for n, score := range scores {
		rows := map[string]map[string]float64{}
		for id, m := range score {
			rows[id] = map[string]float64{"close": 10, "volume": 100, "m": m}
		}
		h.frame(n+1, rows)
	}
	return h
}

// 追赶期对 hold 规则也求值、推进入场序号与冷却：放行后的结果与连续运行一致。
func TestCatchupAdvancesHoldState(t *testing.T) {
	control := newHoldHarness(t)
	for n := 1; n <= 5; n++ {
		control.now = barStart(n).Add(90 * time.Minute)
		expectAck(t, control.deliver(n))
	}
	h := newHoldHarness(t)
	mode := ModeLive
	h.handler.Gate = func(string) Mode { return mode }
	h.now = barStart(1).Add(90 * time.Minute)
	expectAck(t, h.deliver(1))
	mode = ModeCatchup
	for n := 2; n <= 4; n++ {
		h.now = barStart(n).Add(90 * time.Minute)
		expectAck(t, h.deliver(n))
		if got := h.resultAt(n); got.Status != store.StatusSkipped || got.SkipReason != store.SkipCatchup || string(got.RuleStatesJSON) == "{}" {
			t.Fatalf("追赶期应记为带规则状态的 skipped(catchup)：%+v", got)
		}
	}
	mode = ModeLive
	h.now = barStart(5).Add(90 * time.Minute)
	expectAck(t, h.deliver(5))
	live, want := h.resultAt(5), control.resultAt(5)
	if live.Status != store.StatusOK || string(live.TargetsJSON) != string(want.TargetsJSON) || string(live.RuleStatesJSON) != string(want.RuleStatesJSON) {
		t.Fatalf("追赶后的结果应与连续运行一致：\n got  %s %s\n want %s %s", live.TargetsJSON, live.RuleStatesJSON, want.TargetsJSON, want.RuleStatesJSON)
	}
}
