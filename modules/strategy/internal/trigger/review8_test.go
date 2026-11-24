package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

// corrupt 直接改写已启用实例的会话快照（存储层的校验写不出损坏的快照，这里模拟数据被意外改坏）。
func corrupt(t *testing.T, h *harness, sessionJSON string) {
	t.Helper()
	h.handler.programs = nil
	if err := h.repo.ApplySchema(`UPDATE t_strategy_sessions SET c_resolved_json = '` + sessionJSON + `' WHERE c_session_id = '` + h.session + `';`); err != nil {
		t.Fatal(err)
	}
}

func stockEvent(eventID string) (*eventpb.EventMessage, *storagepb.ViewDataReady) {
	message := &eventpb.EventMessage{EventId: eventID, EventName: "event.storage.view.data_ready", SpaceId: "space"}
	// A 股日线的周期时间是日期键：交易日期的 UTC 零点。
	payload := &storagepb.ViewDataReady{ViewId: "view_factor_a", Frequency: "1d", PeriodTime: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC).Unix(), Status: "complete", UniverseSubjectIds: []string{"600000.SH"}}
	return message, payload
}

// 会话快照损坏时没有日历与周期：只计数、不写记录（不按 crypto 写错周期）。
func TestCorruptSessionSnapshotOnlyCounts(t *testing.T) {
	broken := newHarness(t, rankDSL, nil)
	corrupt(t, broken, `{}`)
	message, payload := stockEvent("event-broken")
	expectAck(t, broken.handler.Handle(context.Background(), message, payload))
	if len(broken.observer.results) != 1 || broken.observer.results[0] != "00:skipped:config_error" {
		t.Fatalf("会话快照损坏时只计数：%v", broken.observer.results)
	}
	if _, found, err := broken.repo.ResultAtBar(context.Background(), "i1", broken.session, time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)); err != nil || found {
		t.Fatalf("会话快照损坏时不写记录：found=%v err=%v", found, err)
	}
}
