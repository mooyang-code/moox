package trigger

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
)

const f1HoldDSL = `name: f1_demo
bar: 1h
universe: {}
rules:
  - id: r
    type: f1
    weight: {total: 1}
    f1:
      hold_period: 3
      long_weight: 2
      short_weight: 0
      long_coin_num: 1
      short_coin_num: 0
      seal999: false
      long_factors: [{factor: "m", descending: true, weight: 1.0}]
      short_factors: []
      filter_before: []
      filter_after: []
portfolio:
  leverage: 10
  max_missing: 1
`

func newF1Harness(t *testing.T) *harness {
	t.Helper()
	resolved := input.Resolved{CompletionKind: events.CollectorPeriodCompleted.Name(), ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds", Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: "f", FactorID: "f", DefinitionHash: "h1"}}, Factors: map[string]string{"f": "h1|fixed"}, ViewColumns: []string{"close", "volume", "m"}}
	h := newHarnessWith(t, f1HoldDSL, nil, resolved)
	// 每期领先的标的轮换：不同周期开不同的仓，漏掉一期就会让持仓记录与持有计数对不上。
	leaders := []string{"A", "B", "C", "A", "B"}
	for n := 1; n <= len(leaders); n++ {
		rows := map[string]map[string]float64{}
		for _, id := range []string{"A", "B", "C"} {
			m := 1.0
			if id == leaders[n-1] {
				m = 9
			}
			rows[id] = map[string]float64{"close": 10 + float64(n), "volume": 100, "m": m}
		}
		h.frame(n, rows)
	}
	return h
}

// 追赶中的周期对 f1 规则也求值、推进状态（记为 skipped(catchup)，不发目标）：放行后第一个正式周期的结果与从未中断一致。
func TestCatchupAdvancesF1StateSoLiveResultMatchesUninterruptedRun(t *testing.T) {
	control := newF1Harness(t)
	for n := 1; n <= 4; n++ {
		control.now = barStart(n).Add(90 * time.Minute)
		expectAck(t, control.deliver(n))
	}

	h := newF1Harness(t)
	mode := ModeLive
	h.handler.Gate = func(string) Mode { return mode }
	h.now = barStart(1).Add(90 * time.Minute)
	expectAck(t, h.deliver(1))
	mode = ModeCatchup
	for n := 2; n <= 3; n++ {
		h.now = barStart(n).Add(90 * time.Minute)
		expectAck(t, h.deliver(n))
		got := h.resultAt(n)
		if got.Status != store.StatusSkipped || got.SkipReason != store.SkipCatchup || len(got.EventData) != 0 || string(got.TargetsJSON) != "[]" {
			t.Fatalf("追赶期应记为不发目标的 skipped(catchup)：%+v", got)
		}
		if string(got.RuleStatesJSON) == "{}" {
			t.Fatalf("追赶期应记录推进后的规则状态")
		}
	}
	mode = ModeLive
	h.now = barStart(4).Add(90 * time.Minute)
	expectAck(t, h.deliver(4))
	live, want := h.resultAt(4), control.resultAt(4)
	if live.Status != store.StatusOK {
		t.Fatalf("放行后第一个周期应正常求值：%+v", live)
	}
	if string(live.TargetsJSON) != string(want.TargetsJSON) {
		t.Fatalf("追赶后的目标应与连续运行一致：\n got  %s\n want %s", live.TargetsJSON, want.TargetsJSON)
	}
	if string(live.RuleStatesJSON) != string(want.RuleStatesJSON) {
		t.Fatalf("追赶后的规则状态应与连续运行一致：\n got  %s\n want %s", live.RuleStatesJSON, want.RuleStatesJSON)
	}
}

// 有效期已过的迟到周期同样推进 f1 状态（系统停机后事件延迟到达的情形），记为 skipped(expired)。
func TestExpiredPeriodAdvancesF1State(t *testing.T) {
	control := newF1Harness(t)
	for n := 1; n <= 3; n++ {
		control.now = barStart(n).Add(90 * time.Minute)
		expectAck(t, control.deliver(n))
	}

	h := newF1Harness(t)
	h.now = barStart(1).Add(90 * time.Minute)
	expectAck(t, h.deliver(1))
	h.now = barStart(2).Add(10 * time.Hour) // 远超有效期
	expectAck(t, h.deliver(2))
	late := h.resultAt(2)
	if late.Status != store.StatusSkipped || late.SkipReason != store.SkipExpired || string(late.RuleStatesJSON) == "{}" {
		t.Fatalf("迟到周期应记为带状态的 skipped(expired)：%+v", late)
	}
	h.now = barStart(3).Add(90 * time.Minute)
	expectAck(t, h.deliver(3))
	if got, want := h.resultAt(3), control.resultAt(3); string(got.RuleStatesJSON) != string(want.RuleStatesJSON) || string(got.TargetsJSON) != string(want.TargetsJSON) {
		t.Fatalf("迟到周期之后的结果应与连续运行一致：\n got  %s\n want %s", got.TargetsJSON, want.TargetsJSON)
	}
}

// 追赶期求值不是 f1 专属：带持仓缓冲的 rank 策略，追赶期的持仓集合也要推进，放行后与连续运行一致。
func TestCatchupAdvancesStateForRankStrategy(t *testing.T) {
	build := func() *harness {
		h := newHarness(t, rankDSL, nil)
		scores := []map[string]float64{
			{"A": 9, "B": 8, "C": 1, "D": 0},
			{"A": 1, "B": 2, "C": 9, "D": 8},
			{"A": 1, "B": 2, "C": 3, "D": 9},
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
	control := build()
	for n := 1; n <= 3; n++ {
		control.now = barStart(n).Add(90 * time.Minute)
		expectAck(t, control.deliver(n))
	}
	h := build()
	mode := ModeLive
	h.handler.Gate = func(string) Mode { return mode }
	h.now = barStart(1).Add(90 * time.Minute)
	expectAck(t, h.deliver(1))
	mode = ModeCatchup
	h.now = barStart(2).Add(90 * time.Minute)
	expectAck(t, h.deliver(2))
	if got := h.resultAt(2); got.SkipReason != store.SkipCatchup || len(got.EventData) != 0 || string(got.RuleStatesJSON) == "{}" {
		t.Fatalf("追赶期应记为带状态、不发目标的 skipped(catchup)：%+v", got)
	}
	mode = ModeLive
	h.now = barStart(3).Add(90 * time.Minute)
	expectAck(t, h.deliver(3))
	if got, want := h.resultAt(3), control.resultAt(3); string(got.RuleStatesJSON) != string(want.RuleStatesJSON) || string(got.TargetsJSON) != string(want.TargetsJSON) {
		t.Fatalf("追赶后的结果应与连续运行一致：\n got  %s\n want %s", got.TargetsJSON, want.TargetsJSON)
	}
}
