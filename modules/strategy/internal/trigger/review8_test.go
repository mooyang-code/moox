package trigger

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

// corrupt 直接改写已启用实例的快照（存储层的校验写不出损坏的快照，这里模拟数据被意外改坏）。
func corrupt(t *testing.T, h *harness, sessionJSON, instanceJSON string) {
	t.Helper()
	h.handler.programs = nil
	statements := `UPDATE t_strategy_sessions SET c_resolved_json = '` + sessionJSON + `' WHERE c_session_id = '` + h.session + `';
UPDATE t_strategy_instances SET c_resolved_json = '` + instanceJSON + `' WHERE c_instance_id = 'i1';`
	if err := h.repo.ApplySchema(statements); err != nil {
		t.Fatal(err)
	}
}

func stockEvent(eventID, kind string) (*eventpb.EventMessage, *storagepb.ViewDataReady) {
	shanghai := time.FixedZone("CST", 8*3600)
	message := &eventpb.EventMessage{EventId: eventID, EventName: "event.storage.view.data_ready", SpaceId: "space"}
	payload := &storagepb.ViewDataReady{ViewId: "view_a", Frequency: "1d", PeriodTime: time.Date(2026, 10, 9, 0, 0, 0, 0, shanghai).Unix(), Status: "complete", UniverseSubjectIds: []string{"600000.SH"}, CompletionKind: kind}
	return message, payload
}

// 类型不符的完成事件先于一切处理被忽略：编译失败的会话不会因此写跳过记录、计数或标记 degraded。
func TestCompileFailedSessionIgnoresOtherCompletionKinds(t *testing.T) {
	brokenDSL := `name: broken
rules:
  - {id: r, type: rank, score: "missing_col", select: {top: 1}, weight: 1}
`
	resolved := input.Resolved{ViewID: "view_a", DatasetID: "ds", Bar: "1d", Calendar: "cn_stock", Spot: true, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close"}, CompletionKind: events.FactorPeriodComputed.Name()}
	h := newHarnessWith(t, brokenDSL, nil, resolved)
	h.now = time.Date(2026, 10, 9, 7, 5, 0, 0, time.UTC)
	message, payload := stockEvent("event-kline", events.CollectorPeriodCompleted.Name())
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	if _, found, err := h.repo.ResultAtBar(context.Background(), "i1", h.session, time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)); err != nil || found {
		t.Fatalf("类型不符的事件不应写记录：found=%v err=%v", found, err)
	}
	if len(h.observer.results) != 0 {
		t.Fatalf("类型不符的事件不应计数：%v", h.observer.results)
	}
}

// 会话快照损坏时退回实例上的副本取日历与周期，跳过记录按 A 股收盘写；两份都坏时只计数、不写记录（不按 crypto 写错周期）。
func TestCorruptSessionSnapshotFallsBackToInstanceCopy(t *testing.T) {
	h := newHarness(t, rankDSL, nil)
	h.now = time.Date(2026, 10, 9, 7, 5, 0, 0, time.UTC)
	stock, _ := json.Marshal(input.Resolved{CompletionKind: events.CollectorPeriodCompleted.Name(), ViewID: "view_a", DatasetID: "ds", Bar: "1d", Calendar: "cn_stock", Spot: true, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close", "m"}})
	corrupt(t, h, `{}`, string(stock))
	message, payload := stockEvent("event-stock", events.CollectorPeriodCompleted.Name())
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	result, found, err := h.repo.ResultAtBar(context.Background(), "i1", h.session, time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC))
	if err != nil || !found || result.Status != store.StatusSkipped || result.SkipReason != input.SkipConfigError {
		t.Fatalf("会话快照损坏时应按实例副本的 A 股周期记跳过：%+v found=%v err=%v", result, found, err)
	}

	broken := newHarness(t, rankDSL, nil)
	corrupt(t, broken, `{}`, `{}`)
	message, payload = stockEvent("event-broken", "")
	expectAck(t, broken.handler.Handle(context.Background(), message, payload))
	if len(broken.observer.results) != 1 || broken.observer.results[0] != "00:skipped:config_error" {
		t.Fatalf("两份快照都坏时只计数：%v", broken.observer.results)
	}
	if _, found, err := broken.repo.ResultAtBar(context.Background(), "i1", broken.session, time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)); err != nil || found {
		t.Fatalf("两份快照都坏时不写记录：found=%v err=%v", found, err)
	}
}
