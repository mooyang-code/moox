package trigger

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
)

// A 股实例的跳过说明里，时间带上海的交易日收盘（与回放页、其余 A 股报错一致），而不是只有 UTC 时间：有效期已过的说明
// 写“（上海 2026-10-13 收盘）”，不用用户自己把 07:00Z 换算成上海的 15:00。
func TestStockSkipDetailCarriesShanghaiClose(t *testing.T) {
	resolved := input.Resolved{CompletionKind: events.CollectorPeriodCompleted.Name(), ViewID: "view_a", DatasetID: "ds", Bar: "1d", Calendar: "cn_stock", Spot: true, Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close", "m"}}
	h := newHarnessWith(t, rankDSL, nil, resolved)
	shanghai := time.FixedZone("CST", 8*3600)
	// 周期 2026-10-09 的有效期到其后第 2 个交易日（10-13）收盘，当前时间已远在其后。
	h.now = time.Date(2026, 11, 2, 10, 0, 0, 0, shanghai)
	message, payload := stockEvent("event-old", events.CollectorPeriodCompleted.Name())
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	result, found, err := h.repo.ResultAtBar(context.Background(), "i1", h.session, time.Date(2026, 10, 9, 15, 0, 0, 0, shanghai))
	if err != nil || !found || result.Status != store.StatusSkipped || result.SkipReason != store.SkipExpired {
		t.Fatalf("有效期已过的周期应记 skipped(expired)：%+v found=%v err=%v", result, found, err)
	}
	var recorded store.InputRecord
	if err := json.Unmarshal(result.InputJSON, &recorded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorded.Detail, "（上海 2026-10-13 收盘）") {
		t.Fatalf("A 股的跳过说明应带上海的交易日收盘：%q", recorded.Detail)
	}
}
