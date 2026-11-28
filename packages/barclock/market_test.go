package barclock

import (
	"errors"
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

// 日终时刻：港股 16:10（等收市竞价，不改变 16:00 的交易时段），其余 session 市场等于最后一个交易时段收盘，24 小时市场为空。
func TestMarketDayClose(t *testing.T) {
	want := map[string]string{
		"crypto":  "",
		"stockcn": "15:00",
		"stockhk": "16:10",
		"stockus": "16:00",
	}
	for _, market := range Markets() {
		expected, ok := want[market.ID]
		if !ok {
			t.Fatalf("未覆盖市场 %s 的日终时刻断言", market.ID)
		}
		if market.DayClose != expected {
			t.Fatalf("%s DayClose = %q，期望 %q", market.ID, market.DayClose, expected)
		}
		if market.Alignment != AlignmentSession {
			if market.DayClose != "" {
				t.Fatalf("全天市场 %s 不应设置 DayClose：%q", market.ID, market.DayClose)
			}
			continue
		}
		parsed, err := time.Parse("15:04", market.DayClose)
		if err != nil {
			t.Fatalf("%s DayClose %q 必须是 HH:MM：%v", market.ID, market.DayClose, err)
		}
		regular, err := market.RegularClose()
		if err != nil {
			t.Fatalf("%s RegularClose：%v", market.ID, err)
		}
		dayCloseOffset := time.Duration(parsed.Hour())*time.Hour + time.Duration(parsed.Minute())*time.Minute
		if dayCloseOffset < regular {
			t.Fatalf("%s DayClose(%v) 早于 RegularClose(%v)", market.ID, dayCloseOffset, regular)
		}
	}
}

func TestMarketAndClockForDataset(t *testing.T) {
	for _, tc := range []struct {
		alignment, calendar string
		wantAlign           string
		wantCalendar        string
		wantTimezone        string
	}{
		{"", "", AlignmentEpochUTC, "", "UTC"},
		{"epoch_utc", "", AlignmentEpochUTC, "", "UTC"},
		{"EPOCH_UTC", "crypto_24x7", AlignmentEpochUTC, "", "UTC"},
		{"session", "cn_stock", AlignmentSession, "cn_stock", "Asia/Shanghai"},
		{"session", "HK_STOCK", AlignmentSession, "hk_stock", "Asia/Hong_Kong"},
		{"session", "us_stock", AlignmentSession, "us_stock", "America/New_York"},
		{"", "cn_stock", AlignmentSession, "cn_stock", "Asia/Shanghai"},
	} {
		market, err := MarketForDataset(tc.alignment, tc.calendar)
		if err != nil {
			t.Fatalf("MarketForDataset(%q, %q) unexpected err: %v", tc.alignment, tc.calendar, err)
		}
		if market.Alignment != tc.wantAlign || market.CalendarID != tc.wantCalendar || market.Timezone != tc.wantTimezone {
			t.Fatalf("MarketForDataset(%q, %q) = %+v", tc.alignment, tc.calendar, market)
		}
		clock, err := ClockForDataset(tc.alignment, tc.calendar, 5*time.Minute)
		if err != nil || clock.Alignment() != tc.wantAlign {
			t.Fatalf("ClockForDataset(%q, %q, 5m) = %v, %v", tc.alignment, tc.calendar, clock, err)
		}
		allowed, err := AllowedIntradayForDataset(tc.alignment, tc.calendar)
		if err != nil || len(allowed) == 0 {
			t.Fatalf("AllowedIntradayForDataset(%q, %q) = %v, %v", tc.alignment, tc.calendar, allowed, err)
		}
	}

	if _, err := MarketForDataset("session", ""); !errors.Is(err, ErrNoCalendar) {
		t.Fatalf("session without calendar should return ErrNoCalendar, got %v", err)
	}
	if _, err := MarketForDataset("session", "jp_stock"); !errors.Is(err, ErrNoCalendar) {
		t.Fatalf("session with unknown calendar should return ErrNoCalendar, got %v", err)
	}
	if _, err := MarketForDataset("epoch_utc", "cn_stock"); err == nil {
		t.Fatal("epoch_utc with session calendar should fail")
	}
	if _, err := MarketForDataset("custom", ""); err == nil {
		t.Fatal("unknown alignment should fail")
	}
}
