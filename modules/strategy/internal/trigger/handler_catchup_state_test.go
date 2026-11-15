package trigger

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// 追赶中的周期对 holding 分批持有的规则也求值、推进状态（记为 skipped(catchup)，不发目标）：放行后第一个正式周期的结果与从未中断一致。
func TestCatchupAdvancesBatchStateSoLiveResultMatchesUninterruptedRun(t *testing.T) {
	control := newBatchHarness(t)
	for n := 1; n <= 4; n++ {
		control.now = barStart(n).Add(90 * time.Minute)
		expectAck(t, control.deliver(n))
	}

	h := newBatchHarness(t)
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
		if h.stateAt(n) == "{}" {
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
	if got, want := h.stateAt(4), control.stateAt(4); got != want {
		t.Fatalf("追赶后的规则状态应与连续运行一致：\n got  %s\n want %s", got, want)
	}
}

// 有效期已过的迟到周期同样推进批次状态（系统停机后事件延迟到达的情形），记为 skipped(expired)。
func TestExpiredPeriodAdvancesBatchState(t *testing.T) {
	control := newBatchHarness(t)
	for n := 1; n <= 3; n++ {
		control.now = barStart(n).Add(90 * time.Minute)
		expectAck(t, control.deliver(n))
	}

	h := newBatchHarness(t)
	h.now = barStart(1).Add(90 * time.Minute)
	expectAck(t, h.deliver(1))
	h.now = barStart(2).Add(10 * time.Hour) // 远超有效期
	expectAck(t, h.deliver(2))
	late := h.resultAt(2)
	if late.Status != store.StatusSkipped || late.SkipReason != store.SkipExpired || h.stateAt(2) == "{}" {
		t.Fatalf("迟到周期应记为带状态的 skipped(expired)：%+v", late)
	}
	h.now = barStart(3).Add(90 * time.Minute)
	expectAck(t, h.deliver(3))
	if got, want := h.resultAt(3), control.resultAt(3); h.stateAt(3) != control.stateAt(3) || string(got.TargetsJSON) != string(want.TargetsJSON) {
		t.Fatalf("迟到周期之后的结果应与连续运行一致：\n got  %s\n want %s", got.TargetsJSON, want.TargetsJSON)
	}
}

// 带持仓缓冲的规则同样如此：追赶期的持仓集合也要推进，放行后与连续运行一致。
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
	if got := h.resultAt(2); got.SkipReason != store.SkipCatchup || len(got.EventData) != 0 || h.stateAt(2) == "{}" {
		t.Fatalf("追赶期应记为带状态、不发目标的 skipped(catchup)：%+v", got)
	}
	mode = ModeLive
	h.now = barStart(3).Add(90 * time.Minute)
	expectAck(t, h.deliver(3))
	if got, want := h.resultAt(3), control.resultAt(3); h.stateAt(3) != control.stateAt(3) || string(got.TargetsJSON) != string(want.TargetsJSON) {
		t.Fatalf("追赶后的结果应与连续运行一致：\n got  %s\n want %s", got.TargetsJSON, want.TargetsJSON)
	}
}
