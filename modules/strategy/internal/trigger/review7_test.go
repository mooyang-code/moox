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

// A 股会话按快照重新编译 DSL 失败时，跳过记录仍按快照的日历与周期换算：bar_end 是交易日 15:00（上海），
// 输入摘要记 cn_stock，而不是退回 crypto 日历与事件里的频率。
func TestCompileFailureKeepsSnapshotCalendar(t *testing.T) {
	brokenDSL := `name: broken
rules:
  - {id: r, type: rank, score: "missing_col", select: {top: 1}, weight: 1}
`
	resolved := input.Resolved{CompletionKind: events.CollectorPeriodCompleted.Name(), ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds", Bar: "1d", Calendar: "cn_stock", Spot: true, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close"}}
	h := newHarnessWith(t, brokenDSL, nil, resolved)
	shanghai := time.FixedZone("CST", 8*3600)
	periodStart := time.Date(2026, 10, 9, 0, 0, 0, 0, shanghai)
	h.now = time.Date(2026, 10, 9, 15, 5, 0, 0, shanghai)
	message := &eventpb.EventMessage{EventId: "event-stock", EventName: "event.storage.view.data_ready", SpaceId: "space"}
	payload := &storagepb.ViewDataReady{CompletionKind: events.CollectorPeriodCompleted.Name(), ViewId: "view_factor_a", Frequency: "1d", PeriodTime: periodStart.Unix(), Status: "complete", UniverseSubjectIds: []string{"600000.SH"}}
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
