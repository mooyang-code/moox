package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
)

// rebindStock 把实例换到一个 A 股日线会话上（停用 → 写会话 → 挂会话 → 启用）。
func (h *harness) rebindStock() {
	h.t.Helper()
	ctx := context.Background()
	if err := h.repo.SetInstanceEnabled(ctx, "i1", false, nil, nil, bar0); err != nil {
		h.t.Fatal(err)
	}
	resolved := input.Resolved{ViewID: "view_a", DatasetID: "ds", Bar: "1d", Calendar: "cn_stock", Spot: true, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close", "m"}}
	raw, _ := json.Marshal(resolved)
	session := "session-stock"
	if err := h.repo.OpenSession(ctx, store.Session{SessionID: session, InstanceID: "i1", DSLHash: dsl.Hash([]byte(rankDSL)), ResolvedJSON: string(raw), CreatedAt: bar0}, rankDSL); err != nil {
		h.t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		if err := h.repo.SetInstanceEnabled(ctx, "i1", enabled, &session, raw, bar0); err != nil {
			h.t.Fatal(err)
		}
	}
	h.session = session
	h.handler = h.newHandler()
}

// A 股日历无法换算的周期（不在交易日上、超出内嵌日历的范围）只能 ACK：写日志并计入模块健康失败，不悄悄丢掉。
func TestHandleLogsUnmappablePeriods(t *testing.T) {
	h := newHarness(t, rankDSL, nil, false)
	h.rebindStock()
	var logs []string
	h.handler.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(day time.Time) error {
		message := &eventpb.EventMessage{EventId: "event-" + day.Format("0102"), EventName: "event.storage.view.data_ready", SpaceId: "space"}
		payload := &storagepb.ViewDataReady{ViewId: "view_a", Frequency: "1d", PeriodTime: day.Unix(), Status: "complete", UniverseSubjectIds: []string{"A"}}
		return h.handler.Handle(context.Background(), message, payload)
	}
	// 国庆假期不是交易日：无法确定周期边界。
	expectAck(t, deliver(time.Date(2026, 10, 5, 0, 0, 0, 0, location)))
	// 内嵌日历只到 2026-12-31：12-30 的有效期要推到 2027 年，无法推算。
	expectAck(t, deliver(time.Date(2026, 12, 30, 0, 0, 0, 0, location)))
	if len(logs) != 2 || !strings.Contains(logs[0], "无法确定周期边界") || !strings.Contains(logs[1], "无法推算有效期") {
		t.Fatalf("两期都应留下日志：%q", logs)
	}
	if len(h.observer.results) != 2 {
		t.Fatalf("两期都应计入模块健康：%v", h.observer.results)
	}
	for _, result := range h.observer.results {
		if !strings.HasSuffix(result, ":skipped:config_error") {
			t.Fatalf("应记为 config_error：%v", h.observer.results)
		}
	}
	if h.loader.calls != 0 {
		t.Fatalf("无法换算的周期不应读取输入：%d", h.loader.calls)
	}
}
