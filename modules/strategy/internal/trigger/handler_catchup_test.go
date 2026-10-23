package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// 恢复追赶：只记录周期（skipped(catchup)，不求值、不发目标），并把因子指纹写进记录，
// 引用上一周期因子列的策略放行后第一个周期能找到相邻记录，不会因 previous_version_unknown 被跳过。
func TestCatchupModeRecordsPeriodsThenFirstLivePeriodEvaluates(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, previousBarDSL, &account)
	for n := 1; n <= 3; n++ {
		h.frame(n, map[string]map[string]float64{"A": {"m": float64(n + 1), "prev_m": float64(n)}})
	}
	mode := ModeCatchup
	h.handler.Gate = func(string) Mode { return mode }

	h.now = barStart(1).Add(90 * time.Minute)
	expectAck(t, h.deliver(1))
	first := h.resultAt(1)
	if first.Status != store.StatusSkipped || first.SkipReason != store.SkipCatchup {
		t.Fatalf("追赶中的周期应记为 skipped(catchup)：%+v", first)
	}
	if len(first.EventData) != 0 {
		t.Fatalf("追赶中不能发目标事件：%+v", first)
	}
	if h.loader.calls != 0 {
		t.Fatalf("追赶中不读输入、不求值：%d", h.loader.calls)
	}

	h.now = barStart(2).Add(90 * time.Minute)
	expectAck(t, h.deliver(2))
	if got := h.resultAt(2); got.SkipReason != store.SkipCatchup {
		t.Fatalf("第二期也只记录：%+v", got)
	}

	// 放行后的第一个周期：相邻周期（上一期）有记录，正常求值。
	mode = ModeLive
	h.now = barStart(3).Add(90 * time.Minute)
	expectAck(t, h.deliver(3))
	live := h.resultAt(3)
	if live.Status != store.StatusOK {
		t.Fatalf("放行后第一个周期应正常求值（相邻周期有记录）：%+v", live)
	}
}

// 追赶中即使有效期已过，周期也要记录（保证相邻周期记录连续）；正常模式下过期的周期记为 expired。
func TestCatchupModeRecordsPeriodsEvenWhenExpired(t *testing.T) {
	h := newHarness(t, previousBarDSL, nil)
	mode := ModeCatchup
	h.handler.Gate = func(string) Mode { return mode }
	h.now = barStart(1).Add(10 * time.Hour) // 远超 2 根的有效期
	expectAck(t, h.deliver(1))
	if got := h.resultAt(1); got.SkipReason != store.SkipCatchup {
		t.Fatalf("追赶中过期的周期也要记录：%+v", got)
	}
	mode = ModeLive
	expectAck(t, h.deliver(2))
	if got := h.resultAt(2); got.SkipReason != store.SkipExpired {
		t.Fatalf("正常模式下过期的周期记为 expired：%+v", got)
	}
}

// 放行边界：放行之前收盘的周期，无论事件何时到达（延迟、重投），都只记录、不求值、不发目标；放行之后收盘的正常求值。
func TestReleasedAtBoundaryOnlyRecordsEarlierPeriods(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, previousBarDSL, &account)
	for n := 1; n <= 3; n++ {
		h.frame(n, map[string]map[string]float64{"A": {"m": float64(n + 1), "prev_m": float64(n)}})
	}
	h.handler.Gate = func(string) Mode { return ModeLive }
	// 放行发生在第 2 根收盘之后、第 3 根收盘之前：第 1、2 根的事件迟到了。
	released := barStart(2).Add(time.Hour).Add(10 * time.Minute)
	h.handler.ReleasedAt = func(string) time.Time { return released }

	h.now = barStart(3).Add(30 * time.Minute) // 第 3 根收盘前后，第 1、2 根仍在有效期内
	expectAck(t, h.deliver(1))
	if got := h.resultAt(1); got.SkipReason != store.SkipCatchup || len(got.EventData) != 0 {
		t.Fatalf("放行之前收盘的周期不能发目标：%+v", got)
	}
	expectAck(t, h.deliver(2))
	if got := h.resultAt(2); got.SkipReason != store.SkipCatchup || len(got.EventData) != 0 {
		t.Fatalf("放行之前收盘的周期不能发目标：%+v", got)
	}
	// 重投同一事件结果不变。
	expectAck(t, h.deliver(2))

	h.now = barStart(3).Add(90 * time.Minute)
	expectAck(t, h.deliver(3))
	if got := h.resultAt(3); got.Status != store.StatusOK {
		t.Fatalf("放行之后收盘的周期正常求值：%+v", got)
	}
}

// 追赶进度：追赶期间到达的事件都被记录，已处理到最新已收盘的 bar 就是追平。
func TestCatchupProgressReachesTheLatestClosedBar(t *testing.T) {
	h := newHarness(t, previousBarDSL, nil)
	h.handler.Gate = func(string) Mode { return ModeCatchup }
	ctx := context.Background()
	now := barStart(4).Add(30 * time.Minute) // 最新已收盘的 bar 以第 3 根结束（bar_end = barStart(3)+1h = barStart(4)）
	h.now = now

	progress, err := h.handler.CatchupProgress(ctx, "i1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Latest.IsZero() || !progress.Expected.Equal(barStart(4)) || progress.CaughtUp() {
		t.Fatalf("还没处理任何周期：%+v", progress)
	}
	expectAck(t, h.deliver(2))
	expectAck(t, h.deliver(3))
	progress, err = h.handler.CatchupProgress(ctx, "i1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Latest.Equal(barStart(4)) || !progress.CaughtUp() {
		t.Fatalf("已处理到最新已收盘的 bar 就是追平：%+v", progress)
	}
}
