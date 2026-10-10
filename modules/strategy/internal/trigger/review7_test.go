package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

// A 股会话按快照重新编译 DSL 失败时，跳过记录仍按快照的日历与周期换算：bar_end 是交易日 15:00（上海），
// 输入摘要记 cn_stock，而不是退回 crypto 日历与事件里的频率。
func TestCompileFailureKeepsSnapshotCalendar(t *testing.T) {
	brokenDSL := `name: broken
rules:
  - {id: r, type: rank, score: "missing_col", select: {top: 1}, weight: 1}
`
	resolved := input.Resolved{ViewID: "view_a", DatasetID: "ds", Bar: "1d", Calendar: "cn_stock", Spot: true, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close"}}
	h := newHarnessWith(t, brokenDSL, nil, resolved)
	shanghai := time.FixedZone("CST", 8*3600)
	periodStart := time.Date(2026, 10, 9, 0, 0, 0, 0, shanghai)
	h.now = time.Date(2026, 10, 9, 15, 5, 0, 0, shanghai)
	message := &eventpb.EventMessage{EventId: "event-stock", EventName: "event.storage.view.data_ready", SpaceId: "space"}
	payload := &storagepb.ViewDataReady{ViewId: "view_a", Frequency: "1d", PeriodTime: periodStart.Unix(), Status: "complete", UniverseSubjectIds: []string{"600000.SH"}}
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	barEnd := time.Date(2026, 10, 9, 15, 0, 0, 0, shanghai)
	result, found, err := h.repo.ResultAtBar(context.Background(), "i1", h.session, barEnd)
	if err != nil || !found {
		t.Fatalf("应按 A 股交易日的收盘记录跳过：found=%v err=%v", found, err)
	}
	var recorded store.InputRecord
	if err := json.Unmarshal(result.InputJSON, &recorded); err != nil {
		t.Fatal(err)
	}
	if result.Status != store.StatusSkipped || result.SkipReason != input.SkipConfigError || recorded.Calendar != "cn_stock" || recorded.Bar != "1d" {
		t.Fatalf("跳过记录应保留快照的日历与周期：%+v input=%+v", result, recorded)
	}
}

// 同一事件里一个实例丢弃（周期无法换算），另一个实例需要重投：再次投递时丢弃的实例不重复计数与记日志。
func TestDroppedPeriodIsCountedOnceAcrossRedeliveries(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	ctx := context.Background()
	definition, err := h.repo.GetDefinition(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.repo.CreateInstance(ctx, store.Instance{InstanceID: "i2", StrategyID: "s1", SpaceID: "space", ViewID: "view_a", CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	// i2 的快照是 A 股日线：按小时对齐的周期时间不是上海零点，周期无法换算，本期丢弃。
	stock := input.Resolved{ViewID: "view_a", DatasetID: "ds", Bar: "1d", Calendar: "cn_stock", Spot: true, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close", "m"}}
	raw, _ := json.Marshal(stock)
	session := "session-2"
	if err := h.repo.OpenSession(ctx, store.Session{SessionID: session, InstanceID: "i2", DSLHash: definition.DSLHash, ResolvedJSON: string(raw), CreatedAt: bar0}, definition.DSLYaml); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetInstanceEnabled(ctx, "i2", false, &session, raw, bar0); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.SetInstanceEnabled(ctx, "i2", true, &session, raw, bar0); err != nil {
		t.Fatal(err)
	}
	h.frame(1, map[string]map[string]float64{"A": {"close": 1, "m": 1}})
	// i1 第一次读取失败需要重投，第二次成功。
	h.loader.errs = []error{errors.New("Storage 暂时不可用")}
	expectRetry(t, h.deliver(1))
	expectAck(t, h.deliver(1))
	drops := 0
	for _, entry := range h.observer.results {
		if entry == "00:skipped:config_error" {
			drops++
		}
	}
	if drops != 1 {
		t.Fatalf("丢弃的实例应只计一次：%v", h.observer.results)
	}
}
