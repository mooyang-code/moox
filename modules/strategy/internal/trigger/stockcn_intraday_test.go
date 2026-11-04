package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
)

// A 股 5m：11:25 那根（bar_end 11:30）的有效期往后推 ValidBars=2 根，跨午休到 13:10，而不是按墙钟的 11:40。
func TestStockIntradayValidityCrossesLunchBreak(t *testing.T) {
	shanghai := time.FixedZone("CST", 8*3600)
	valid, err := advance("cn_stock", "5m", time.Date(2026, 10, 8, 11, 30, 0, 0, shanghai), ValidBars)
	if err != nil || !valid.Equal(time.Date(2026, 10, 8, 13, 10, 0, 0, shanghai)) {
		t.Fatalf("11:25 那根的有效期应到 13:10：%s err=%v", valid.In(shanghai), err)
	}

	// 触发入口：5m 的事件按交易时段的 bar_start 换算周期，有效期过了记 skipped(expired)，有效期写 13:10。
	resolved := input.Resolved{ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds", Bar: "5m", Calendar: "cn_stock", MarketType: "equity", Spot: true, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close", "m"}}
	h := newHarnessWith(t, rankDSL, nil, resolved)
	h.now = time.Date(2026, 10, 8, 14, 0, 0, 0, shanghai)
	message := &eventpb.EventMessage{EventId: "event-1125", EventName: "event.storage.view.data_ready", SpaceId: "space"}
	payload := &storagepb.ViewDataReady{ViewId: "view_factor_a", Frequency: "5m", PeriodTime: time.Date(2026, 10, 8, 11, 25, 0, 0, shanghai).Unix(), Status: "complete", UniverseSubjectIds: []string{"600000.SH"}}
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	result, found, err := h.repo.ResultAtBar(context.Background(), "i1", h.session, time.Date(2026, 10, 8, 11, 30, 0, 0, shanghai))
	if err != nil || !found {
		t.Fatalf("应写下 11:25 那根的结果：found=%v err=%v", found, err)
	}
	if result.Status != store.StatusSkipped || result.SkipReason != store.SkipExpired || !result.ValidUntil.Equal(time.Date(2026, 10, 8, 13, 10, 0, 0, shanghai)) {
		t.Fatalf("14:00 时 11:25 那根已过有效期 13:10：%+v", result)
	}
}
