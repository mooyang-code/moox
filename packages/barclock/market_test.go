package barclock

import (
	"testing"
	"time"
)

func TestMarketsSortedAndComplete(t *testing.T) {
	got := Markets()
	if len(got) != len(markets) {
		t.Fatalf("Markets() 返回 %d 个，登记了 %d 个", len(got), len(markets))
	}
	for i, market := range got {
		if registered, ok := markets[market.ID]; !ok || registered.Alignment != market.Alignment {
			t.Fatalf("Markets()[%d]=%+v 与登记不一致", i, market)
		}
		if i > 0 && got[i-1].ID >= market.ID {
			t.Fatalf("Markets() 没有按 ID 排序：%s 在 %s 之后", market.ID, got[i-1].ID)
		}
	}
}

// 按交易日历 ID 找市场：策略只知道日历 ID（cn_stock、hk_stock），据此取交易时段、时区和给人看的名称。
func TestMarketForCalendar(t *testing.T) {
	for calendarID, want := range map[string]struct{ id, name, zone string }{
		"cn_stock":   {"stockcn", "A 股", "上海"},
		" HK_Stock ": {"stockhk", "港股", "香港"},
		"us_stock":   {"stockus", "美股", "纽约"},
	} {
		market, ok := MarketForCalendar(calendarID)
		if !ok || market.ID != want.id || market.Name != want.name || market.ZoneName != want.zone {
			t.Fatalf("MarketForCalendar(%q) = %+v, %v", calendarID, market, ok)
		}
	}
	for _, calendarID := range []string{"", "crypto_24x7", "crypto", "jp_stock"} {
		if _, ok := MarketForCalendar(calendarID); ok {
			t.Fatalf("%q 不是任何 session 市场的日历", calendarID)
		}
	}
}

// 完整交易日的收市时刻与日内桶数：A 股 15:00、5m 48 根，港股 16:00、66 根，美股 16:00、78 根。
func TestRegularCloseAndFullDayBuckets(t *testing.T) {
	for id, want := range map[string]struct {
		close   time.Duration
		buckets int
	}{
		"stockcn": {15 * time.Hour, 48},
		"stockhk": {16 * time.Hour, 66},
		"stockus": {16 * time.Hour, 78},
	} {
		market, err := MarketFor(id)
		if err != nil {
			t.Fatal(err)
		}
		closeAt, err := market.RegularClose()
		if err != nil || closeAt != want.close {
			t.Fatalf("%s RegularClose = %v, %v", id, closeAt, err)
		}
		if got := market.FullDayBuckets(5 * time.Minute); got != want.buckets {
			t.Fatalf("%s 5m 每天 %d 根，期望 %d", id, got, want.buckets)
		}
	}
	crypto, _ := MarketFor("crypto")
	if _, err := crypto.RegularClose(); err == nil || crypto.FullDayBuckets(time.Minute) != 0 {
		t.Fatal("24 小时连续市场没有收市时刻和每日桶数")
	}
}
