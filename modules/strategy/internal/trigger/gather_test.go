package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
)

const dualRankDSL = `name: dual_demo
market: {select: both, prefer: swap}
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

const (
	spotView = "view_factor_a"
	swapView = "view_factor_b"
)

func newDualHarness(t *testing.T) *harness {
	t.Helper()
	resolved := input.Resolved{Market: dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSwap}, Tasks: map[string]input.TaskBinding{
		"spot": {ViewID: spotView, FactorTaskID: "ftask_a", DatasetID: "ds"}, "swap": {ViewID: swapView, FactorTaskID: "ftask_b", DatasetID: "ds2"}},
		Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: "f", FactorID: "f", DefinitionHash: "h1"}},
		Factors: map[string]string{"f": "h1|fixed"}, ViewColumns: []string{"close", "m"}}
	h := newHarnessWith(t, dualRankDSL, nil, resolved)
	h.frame(1, map[string]map[string]float64{"A": {"m": 3}, "B": {"m": 1}})
	h.frame(2, map[string]map[string]float64{"A": {"m": 1}, "B": {"m": 3}})
	return h
}

// viewEvent 构造某个 View 某一期的就绪事件；status 是因子成员状态，hash 是事件里的成员定义哈希。
func (h *harness) viewEvent(view string, n int, status, hash string) (*eventpb.EventMessage, *storagepb.ViewDataReady) {
	message, payload := h.event(n, map[string]string{"f": hash}, status)
	payload.ViewId = view
	message.EventId = fmt.Sprintf("%s-%d", view, n)
	return message, payload
}

func (h *harness) deliverView(view string, n int) error {
	message, payload := h.viewEvent(view, n, "complete", "h1")
	return h.handler.Handle(context.Background(), message, payload)
}

func (h *harness) resultCount() int {
	h.t.Helper()
	results, _, err := h.repo.ListResults(context.Background(), "i1", "", 0, 100)
	if err != nil {
		h.t.Fatal(err)
	}
	return len(results)
}

// 两个 View 都到才求值一次：先到的只记录并确认，后到的触发求值；重复投递不重复求值。
func TestGatherEvaluatesOnceWhenBothViewsArrived(t *testing.T) {
	h := newDualHarness(t)
	expectAck(t, h.deliverView(spotView, 1))
	h.noResultAt(1)
	expectAck(t, h.deliverView(swapView, 1))
	result := h.resultAt(1)
	if result.Status != store.StatusOK {
		t.Fatalf("两个 View 到齐应求值：%+v", result)
	}
	if h.loader.calls != 1 {
		t.Fatalf("应只装配一次：%d", h.loader.calls)
	}
	if len(h.loader.lastBar.EventUniverse) != 2 || len(h.loader.lastBar.Readiness) != 2 || len(h.loader.lastBar.Unavailable) != 0 {
		t.Fatalf("装配参数应带两个市场的名单与结论：%+v", h.loader.lastBar)
	}
	expectAck(t, h.deliverView(spotView, 1))
	expectAck(t, h.deliverView(swapView, 1))
	if h.resultCount() != 1 || h.loader.calls != 1 {
		t.Fatalf("重复投递不应重复求值：results=%d calls=%d", h.resultCount(), h.loader.calls)
	}
	var record store.InputRecord
	if err := jsonUnmarshal(result.InputJSON, &record); err != nil || len(record.Views) != 2 || record.Views[spotView].Factors["f"] != "h1|fixed" || record.Views[swapView].Factors["f"] != "h1|fixed" {
		t.Fatalf("结果的输入记录应每个 View 一份因子指纹：%s err=%v", result.InputJSON, err)
	}
}

// 两个 View 稳定相差一根以上（永续更慢）时，每期照常出目标，不补记 skipped。
func TestGatherToleratesStableLag(t *testing.T) {
	h := newDualHarness(t)
	expectAck(t, h.deliverView(spotView, 1))
	expectAck(t, h.deliverView(spotView, 2))
	h.noResultAt(1)
	h.noResultAt(2)
	expectAck(t, h.deliverView(swapView, 1))
	if h.resultAt(1).Status != store.StatusOK {
		t.Fatal("第 1 期到齐应出目标")
	}
	expectAck(t, h.deliverView(swapView, 2))
	if h.resultAt(2).Status != store.StatusOK || h.resultCount() != 2 {
		t.Fatalf("第 2 期应照常出目标：%d", h.resultCount())
	}
}

// 缺的 View 已经到了更晚的一期（它跳过了这一期）：这一期补记 skipped(market_missing)，不计入实例故障。
func TestGatherMarksPeriodSkippedWhenMissingViewMovedOn(t *testing.T) {
	h := newDualHarness(t)
	expectAck(t, h.deliverView(spotView, 1))
	expectAck(t, h.deliverView(swapView, 2))
	skipped := h.resultAt(1)
	if skipped.Status != store.StatusSkipped || skipped.SkipReason != store.SkipMarketMissing {
		t.Fatalf("第 1 期应补记 market_missing：%+v", skipped)
	}
	h.noResultAt(2)
	expectAck(t, h.deliverView(spotView, 2))
	if h.resultAt(2).Status != store.StatusOK {
		t.Fatal("第 2 期到齐应出目标")
	}
	instance, err := h.repo.GetInstance(context.Background(), "i1")
	if err != nil || instance.Health != store.HealthOK {
		t.Fatalf("market_missing 不计入实例故障：%+v err=%v", instance, err)
	}
	// 迟到的那一半事件到达时，那一期已经补记过，按现有规则不再求值。
	expectAck(t, h.deliverView(swapView, 1))
	if h.resultAt(1).SkipReason != store.SkipMarketMissing || h.resultCount() != 2 {
		t.Fatalf("补记过的期不应再求值：%d", h.resultCount())
	}
}

// 更晚的一期已经汇合求值时，更早还缺一半的期补记 skipped，并且发生在提交更晚一期之前。
func TestGatherMarksOlderPeriodSkippedWhenLaterPeriodCompleted(t *testing.T) {
	h := newDualHarness(t)
	expectAck(t, h.deliverView(spotView, 1))
	expectAck(t, h.deliverView(spotView, 2))
	expectAck(t, h.deliverView(swapView, 2))
	if h.resultAt(2).Status != store.StatusOK {
		t.Fatal("第 2 期应求值")
	}
	if skipped := h.resultAt(1); skipped.SkipReason != store.SkipMarketMissing {
		t.Fatalf("更早的期应补记 market_missing：%+v", skipped)
	}
}

func (h *harness) deliverStatus(view string, n int, status, hash string) error {
	message, payload := h.viewEvent(view, n, status, hash)
	return h.handler.Handle(context.Background(), message, payload)
}

// 数据类原因（某个市场的因子被跳过）：只让这个市场本期不可用；两个市场都被跳过则整期按原因跳过。
func TestGatherDataReasonOnlyMakesOneMarketUnavailable(t *testing.T) {
	h := newDualHarness(t)
	expectAck(t, h.deliverView(spotView, 1))
	expectAck(t, h.deliverStatus(swapView, 1, "skipped", "h1"))
	if h.resultAt(1).Status != store.StatusOK {
		t.Fatalf("只有永续被跳过时现货照常出目标：%+v", h.resultAt(1))
	}
	if reason, ok := h.loader.lastBar.Unavailable["swap"]; !ok || reason == "" || len(h.loader.lastBar.Unavailable) != 1 {
		t.Fatalf("永续应标为不可用：%+v", h.loader.lastBar.Unavailable)
	}
	expectAck(t, h.deliverStatus(spotView, 2, "skipped", "h1"))
	expectAck(t, h.deliverStatus(swapView, 2, "skipped", "h1"))
	both := h.resultAt(2)
	if both.Status != store.StatusSkipped || both.SkipReason != readiness.ReasonFactorSkipped {
		t.Fatalf("两个市场都被跳过应整期按原因跳过：%+v", both)
	}
}

// 配置、契约类原因（成员契约变了）：整期按原因处理，不论另一个市场是否正常。
func TestGatherContractReasonSkipsWholePeriod(t *testing.T) {
	h := newDualHarness(t)
	expectAck(t, h.deliverView(spotView, 1))
	expectAck(t, h.deliverStatus(swapView, 1, "complete", "h2"))
	result := h.resultAt(1)
	if result.Status != store.StatusSkipped || result.SkipReason != readiness.ReasonFactorChanged {
		t.Fatalf("契约变了应整期跳过：%+v", result)
	}
}

// 门禁返回等待时：事件返回重投，不确认；到达记录照写，这样先到的一半不会丢。
func TestGatherWaitModeKeepsArrivalRecord(t *testing.T) {
	h := newDualHarness(t)
	h.handler.Gate = func(string) Mode { return ModeWait }
	message, payload := h.viewEvent(spotView, 1, "complete", "h1")
	err := h.handler.Handle(context.Background(), message, payload)
	if !errors.Is(err, ErrModuleNotReady) {
		t.Fatalf("等待应返回重投：%v", err)
	}
	records, err := h.repo.ReadyEventsAt(context.Background(), "i1", h.session, barStart(1).Add(time.Hour))
	if err != nil || len(records) != 1 {
		t.Fatalf("等待期间到达记录照写：%+v err=%v", records, err)
	}
	h.handler.Gate = nil
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	expectAck(t, h.deliverView(swapView, 1))
	if h.resultAt(1).Status != store.StatusOK {
		t.Fatal("放行后两个 View 到齐应求值")
	}
	// ModeOff 照旧直接确认、不写记录。
	h.handler.Gate = func(string) Mode { return ModeOff }
	expectAck(t, h.deliverView(spotView, 2))
	records, _ = h.repo.ReadyEventsAt(context.Background(), "i1", h.session, barStart(2).Add(time.Hour))
	if len(records) != 0 {
		t.Fatalf("ModeOff 不写记录：%+v", records)
	}
}

func jsonUnmarshal(raw []byte, out any) error { return json.Unmarshal(raw, out) }

// 门禁等待期间第 1、2 期的事件都已记下；放行后先重投第 2 期：第 1 期两个 View 都到齐了，只是还没求值，
// 必须先按 bar 顺序求值，不能被补记成 market_missing。
func TestGatherProcessesCompletePendingPeriodsBeforeLaterOne(t *testing.T) {
	h := newDualHarness(t)
	h.handler.Gate = func(string) Mode { return ModeWait }
	for _, n := range []int{1, 2} {
		for _, view := range []string{spotView, swapView} {
			message, payload := h.viewEvent(view, n, "complete", "h1")
			if err := h.handler.Handle(context.Background(), message, payload); !errors.Is(err, ErrModuleNotReady) {
				t.Fatalf("等待应返回重投：%v", err)
			}
		}
	}
	h.handler.Gate = nil
	// 放行后先重投第 2 期。
	expectAck(t, h.deliverView(spotView, 2))
	first, second := h.resultAt(1), h.resultAt(2)
	if first.Status != store.StatusOK || first.SkipReason != "" {
		t.Fatalf("第 1 期应按顺序求值出目标，而不是补记：%+v", first)
	}
	if second.Status != store.StatusOK {
		t.Fatalf("第 2 期应出目标：%+v", second)
	}
	if h.loader.calls != 2 || h.resultCount() != 2 {
		t.Fatalf("两期各求值一次：calls=%d results=%d", h.loader.calls, h.resultCount())
	}
	if !first.BarEndTime.Before(second.BarEndTime) || first.CreatedAt.After(second.CreatedAt) {
		t.Fatalf("应先提交第 1 期再提交第 2 期：%v %v", first.CreatedAt, second.CreatedAt)
	}
	// 其余重投的事件都只是确认。
	for _, view := range []string{swapView, spotView} {
		expectAck(t, h.deliverView(view, 1))
	}
	expectAck(t, h.deliverView(swapView, 2))
	if h.resultCount() != 2 || h.loader.calls != 2 {
		t.Fatalf("重复投递不应重复求值：results=%d calls=%d", h.resultCount(), h.loader.calls)
	}
}
