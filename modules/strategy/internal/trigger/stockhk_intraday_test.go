package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
)

// 港股 5m 的实例能求值：11:55 那根（bar_end 12:00，午休前最后一根）在 12:00 过后求值成功，目标按股票市场（STOCK）
// 写标的 ID，有效期按交易序号往后 2 根，跨午休到 13:10；半日市（2026-12-24）11:55 那根的有效期到下一个交易日开盘后。
func TestHKIntradayEvaluatesAcrossLunchBreak(t *testing.T) {
	hk, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		t.Fatal(err)
	}
	account := "logical-hk"
	resolved := input.Resolved{Market: dsl.SingleMarket("equity"), Tasks: map[string]input.TaskBinding{"equity": {ViewID: "view_factor_hk_5m", FactorTaskID: "ftask_hk_5m", DatasetID: "dataset_factor_hk_5m"}}, Bar: "5m", Calendar: "hk_stock", Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{}, ViewColumns: []string{"close", "m"}}
	h := newHarnessWith(t, rankDSL, &account, resolved)
	barStart := time.Date(2026, 11, 17, 11, 55, 0, 0, hk)
	h.loader.frames[barStart.Unix()] = map[string]map[string]float64{
		"00700.XHKG": {"close": 376, "m": 3},
		"00005.XHKG": {"close": 138.5, "m": 2},
		"09988.XHKG": {"close": 85.45, "m": 1},
	}
	h.now = time.Date(2026, 11, 17, 12, 0, 6, 0, hk)
	message := &eventpb.EventMessage{EventId: "event-hk-1155", EventName: "event.storage.view.data_ready", SpaceId: "space"}
	payload := &storagepb.ViewDataReady{ViewId: "view_factor_hk_5m", Frequency: "5m", PeriodTime: barStart.Unix(), Status: "complete", UniverseSubjectIds: []string{"00700.XHKG", "00005.XHKG", "09988.XHKG"}}
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	result, found, err := h.repo.ResultAtBar(context.Background(), "i1", h.session, time.Date(2026, 11, 17, 12, 0, 0, 0, hk))
	if err != nil || !found {
		t.Fatalf("应写下 11:55 那根的结果：found=%v err=%v", found, err)
	}
	if result.Status != store.StatusOK || !result.ValidUntil.Equal(time.Date(2026, 11, 17, 13, 10, 0, 0, hk)) {
		t.Fatalf("港股 11:55 那根应求值成功、有效期到 13:10：status=%s skip=%s valid=%s", result.Status, result.SkipReason, result.ValidUntil.In(hk))
	}
	if data := string(result.EventData); !strings.Contains(data, "00700.XHKG") || !strings.Contains(data, "STOCK") {
		t.Fatalf("目标应带港股标的 ID 与股票市场：%s", data)
	}

	valid, err := advance("hk_stock", "5m", time.Date(2026, 12, 24, 12, 0, 0, 0, hk), ValidBars)
	if err != nil {
		t.Fatal(err)
	}
	if local := valid.In(hk); local.Hour() != 9 || local.Minute() != 40 || local.Day() == 24 {
		t.Fatalf("半日市最后一根的有效期应到下一个交易日 09:40：%s", local)
	}
}
