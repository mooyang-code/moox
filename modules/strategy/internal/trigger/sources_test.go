package trigger

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
)

const sourceRankDSL = `name: source_demo
bar: 1h
sources:
  daily: {bar: 1d}
rules:
  - id: r
    type: rank
    filter: "daily.g > 0"
    score: "m"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

const dailyView = "view_factor_d"

// alignedBar 是收盘时刻（bar0 + 24h，即次日 00:00）与日线收盘对齐的那根 1h 主帧的序号。
const alignedBar = 23

// newSourceHarness 是 1h 主帧（view_factor_a）加 1d 来源 daily（view_factor_d，成员 g）的实例。
func newSourceHarness(t *testing.T) *harness {
	t.Helper()
	return newSourceHarnessWith(t, sourceRankDSL)
}

func newSourceHarnessWith(t *testing.T, dslYaml string) *harness {
	t.Helper()
	resolved := input.Resolved{Market: dsl.SingleMarket("spot"), Tasks: map[string]input.TaskBinding{"spot": {ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds"}},
		Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: "f", FactorID: "f", DefinitionHash: "h1"}},
		Factors: map[string]string{"f": "h1|fixed"}, ViewColumns: []string{"close", "m"},
		Sources: map[string]input.SourceBinding{"daily": {Bar: "1d", Calendar: input.DefaultCalendar,
			Tasks:   map[string]input.TaskBinding{"spot": {ViewID: dailyView, FactorTaskID: "ftask_d", DatasetID: "ds_d"}},
			Columns: map[string]input.ColumnBinding{"g": {Source: input.SourceFactor, Member: "g", FactorID: "g", DefinitionHash: "h2"}},
			Factors: map[string]string{"g": "h2|fixed"}, ViewColumns: []string{"close", "g"}}}}
	h := newHarnessWith(t, dslYaml, nil, resolved)
	for _, n := range []int{1, 2, alignedBar, alignedBar + 1} {
		h.frame(n, map[string]map[string]float64{"A": {"m": 3, "daily.g": 1}, "B": {"m": 1, "daily.g": 1}})
	}
	h.loader.sourceRevisions = map[string]uint64{dailyView: 42}
	return h
}

// dailyEvent 构造日线来源的就绪事件：period_time 是收盘于 alignedBar 的那根日线的 bar_start（bar0）。
func (h *harness) dailyEvent(status, hash string) (*eventpb.EventMessage, *storagepb.ViewDataReady) {
	message, payload := h.event(0, map[string]string{"g": hash}, status)
	payload.ViewId, payload.Frequency, payload.PeriodTime = dailyView, "1d", bar0.Unix()
	message.EventId = fmt.Sprintf("%s-%s-%s", dailyView, status, hash)
	return message, payload
}

func (h *harness) deliverDaily(status, hash string) error {
	message, payload := h.dailyEvent(status, hash)
	return h.handler.Handle(context.Background(), message, payload)
}

// atBar 把“现在”拨到第 n 根收盘之后一分钟（有效期内）。
func (h *harness) atBar(n int) { h.now = barStart(n).Add(time.Hour + time.Minute) }

func (h *harness) inputRecord(n int) store.InputRecord {
	h.t.Helper()
	var record store.InputRecord
	if err := jsonUnmarshal(h.resultAt(n).InputJSON, &record); err != nil {
		h.t.Fatal(err)
	}
	return record
}

// 非汇合 bar（主帧收盘时刻不是日线收盘时刻）只等主帧：到达即求值；输入记录里记下来源读到的修订号。
func TestSourceNonAlignedBarDoesNotWait(t *testing.T) {
	h := newSourceHarness(t)
	h.atBar(1)
	expectAck(t, h.deliver(1))
	if result := h.resultAt(1); result.Status != store.StatusOK {
		t.Fatalf("非汇合 bar 应直接求值：%+v", result)
	}
	record := h.inputRecord(1)
	if record.Views[dailyView].Revision != 42 || record.Views[dailyView].EventID != "" {
		t.Fatalf("非汇合 bar 只记来源的修订号：%+v", record.Views)
	}
	if h.loader.calls != 1 || len(h.loader.lastBar.SourceDown) != 0 {
		t.Fatalf("装配一次、来源可用：calls=%d %+v", h.loader.calls, h.loader.lastBar.SourceDown)
	}
}

// 收盘时刻对齐的那根要等主帧与来源都到齐：先到主帧只记录，来源到达后汇合求值；输入记录里有来源的事件、修订号与指纹。
func TestSourceAlignedBarWaitsForSource(t *testing.T) {
	h := newSourceHarness(t)
	h.atBar(alignedBar)
	expectAck(t, h.deliver(alignedBar))
	h.noResultAt(alignedBar)
	expectAck(t, h.deliverDaily("complete", "h2"))
	if result := h.resultAt(alignedBar); result.Status != store.StatusOK {
		t.Fatalf("两边到齐应求值：%+v", result)
	}
	view := h.inputRecord(alignedBar).Views[dailyView]
	if view.EventID == "" || view.Revision != 42 || view.Factors["g"] != "h2|fixed" {
		t.Fatalf("汇合 bar 应记下来源的事件、修订号与指纹：%+v", view)
	}
	// 重复投递不重复求值。
	expectAck(t, h.deliverDaily("complete", "h2"))
	expectAck(t, h.deliver(alignedBar))
	if h.loader.calls != 1 || h.resultCount() != 1 {
		t.Fatalf("重复投递不应重复求值：calls=%d results=%d", h.loader.calls, h.resultCount())
	}
}

// 来源先到、主帧后到：主帧到达时汇合求值。
func TestSourceArrivesBeforeMain(t *testing.T) {
	h := newSourceHarness(t)
	h.atBar(alignedBar)
	expectAck(t, h.deliverDaily("complete", "h2"))
	h.noResultAt(alignedBar)
	expectAck(t, h.deliver(alignedBar))
	if result := h.resultAt(alignedBar); result.Status != store.StatusOK {
		t.Fatalf("主帧到达后应汇合求值：%+v", result)
	}
}

// 来源缺这一期：下一根主帧（非汇合 bar，到达即完整）求值前，把对齐的那根补记为 skipped(market_missing)。
func TestSourceMissingMarksAlignedBarSkipped(t *testing.T) {
	h := newSourceHarness(t)
	h.atBar(alignedBar)
	expectAck(t, h.deliver(alignedBar))
	h.atBar(alignedBar + 1)
	expectAck(t, h.deliver(alignedBar+1))
	skipped := h.resultAt(alignedBar)
	if skipped.Status != store.StatusSkipped || skipped.SkipReason != store.SkipMarketMissing || !strings.Contains(string(skipped.InputJSON), "附加来源（daily）的数据未到") {
		t.Fatalf("缺来源的那根应补记 market_missing：%+v %s", skipped, skipped.InputJSON)
	}
	if h.resultAt(alignedBar+1).Status != store.StatusOK {
		t.Fatal("下一根照常求值")
	}
	// 来源迟到：这一根已经处理过，直接确认，不再求值。
	calls := h.loader.calls
	expectAck(t, h.deliverDaily("complete", "h2"))
	if h.loader.calls != calls {
		t.Fatalf("已处理的那根不再求值：%d → %d", calls, h.loader.calls)
	}
}

// 来源的因子指纹变化在汇合 bar 上触发 factor_changed；来源的因子被整体跳过时只让来源本期不可用（列按缺数处理），照常求值。
func TestSourceReadinessOnAlignedBar(t *testing.T) {
	h := newSourceHarness(t)
	h.atBar(alignedBar)
	expectAck(t, h.deliver(alignedBar))
	expectAck(t, h.deliverDaily("complete", "h_changed"))
	if result := h.resultAt(alignedBar); result.Status != store.StatusSkipped || result.SkipReason != readiness.ReasonFactorChanged {
		t.Fatalf("来源指纹变化应记 factor_changed：%+v", result)
	}

	down := newSourceHarness(t)
	down.atBar(alignedBar)
	expectAck(t, down.deliver(alignedBar))
	expectAck(t, down.deliverDaily("skipped", "h2"))
	if result := down.resultAt(alignedBar); result.Status != store.StatusOK {
		t.Fatalf("来源的因子被跳过时照常求值：%+v", result)
	}
	if down.loader.lastBar.SourceDown["daily"] == "" {
		t.Fatalf("来源应本期不可用：%+v", down.loader.lastBar.SourceDown)
	}
}

// 只绑一个主帧任务、没有来源的实例走原路径：到达即求值，不写到达记录。
func TestSingleTaskInstanceWritesNoArrival(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.frame(1, map[string]map[string]float64{"A": {"m": 3}, "B": {"m": 1}})
	expectAck(t, h.deliver(1))
	if h.resultAt(1).Status != store.StatusOK {
		t.Fatal("应直接求值")
	}
	records, err := h.repo.ReadyEventsAt(context.Background(), "i1", h.session, barStart(1).Add(time.Hour))
	if err != nil || len(records) != 0 {
		t.Fatalf("单任务实例不应写到达记录：%d %v", len(records), err)
	}
}

// 参考市场的事件只确认，不求值、不计入健康失败。
func TestReferenceViewEventIgnored(t *testing.T) {
	resolved := input.Resolved{Market: dsl.SingleMarket("swap"), Tasks: map[string]input.TaskBinding{"swap": {ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds"}},
		References: map[string]input.TaskBinding{"spot": {ViewID: "view_factor_r", FactorTaskID: "ftask_r", DatasetID: "ds_r"}},
		Bar:        "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: "f", FactorID: "f", DefinitionHash: "h1"}},
		Factors: map[string]string{"f": "h1|fixed"}, ViewColumns: []string{"close", "m"}}
	referenceDSL := "name: ref\nmarket: {select: swap, reference: [spot]}\nuniverse: {min_age_bars: 1, age_markets: all}\nrules:\n  - {id: r, type: rank, score: m, select: {top: 1}, weight: 1}\nportfolio: {max_missing: 1}\n"
	h := newHarnessWith(t, referenceDSL, nil, resolved)
	message, payload := h.event(1, map[string]string{"f": "h1"})
	payload.ViewId = "view_factor_r"
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	if h.resultCount() != 0 || h.loader.calls != 0 || len(h.observer.results) != 0 {
		t.Fatalf("参考市场的事件不应求值或记录：results=%d calls=%d observed=%v", h.resultCount(), h.loader.calls, h.observer.results)
	}
}

// 写了附加来源、又经 bars[-1] 读主帧因子的策略：输入记录里多出的来源 View 条目不影响相邻周期检查，第二根照常求值。
func TestSourceWithPreviousBarKeepsAdjacentCheck(t *testing.T) {
	h := newSourceHarnessWith(t, strings.Replace(sourceRankDSL, `score: "m"`, `score: "m - bars[-1].m"`, 1))
	h.frame(1, map[string]map[string]float64{"A": {"m": 3, "prev_m": 1, "daily.g": 1}, "B": {"m": 1, "prev_m": 2, "daily.g": 1}})
	h.frame(2, map[string]map[string]float64{"A": {"m": 1, "prev_m": 3, "daily.g": 1}, "B": {"m": 3, "prev_m": 1, "daily.g": 1}})
	h.atBar(1)
	expectAck(t, h.deliver(1))
	h.atBar(2)
	expectAck(t, h.deliver(2))
	if result := h.resultAt(2); result.Status != store.StatusOK {
		t.Fatalf("第二根应照常求值（相邻记录可信）：%+v", result)
	}
}
