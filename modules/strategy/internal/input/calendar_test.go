package input

import (
	"testing"
	"time"
)

func TestClosedPeriodCryptoUsesMostRecentClosedBoundary(t *testing.T) {
	trigger := time.Date(2026, 8, 29, 11, 7, 30, 0, time.UTC)
	got, err := ClosedPeriod("crypto_24x7", "1h", trigger)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	if !got.BarEnd.Equal(want) || !got.StorageStart.Equal(want.Add(-time.Hour)) {
		t.Fatalf("最近闭合的 bar 不符：%+v，期望 bar_end %s", got, want)
	}
}

func TestStockPeriodSkipsWeekendAndKeepsEventScheduleIdentity(t *testing.T) {
	// 周一收盘前求值的仍是周五：携带周五零点 Storage 键的事件必须解析到同一个收盘边界。
	trigger := time.Date(2026, 8, 31, 5, 0, 0, 0, time.UTC) // 13:00 Shanghai
	scheduled, err := ClosedPeriod("cn_stock", "1d", trigger)
	if err != nil {
		t.Fatal(err)
	}
	event, err := FromStorageStart("cn_stock", "1d", scheduled.StorageStart)
	if err != nil {
		t.Fatal(err)
	}
	if !scheduled.BarEnd.Equal(event.BarEnd) || !scheduled.StorageStart.Equal(event.StorageStart) {
		t.Fatalf("调度周期与事件周期不一致：调度 %+v，事件 %+v", scheduled, event)
	}
	wantEnd := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	if !scheduled.BarEnd.Equal(wantEnd) {
		t.Fatalf("bar_end = %s，期望 %s", scheduled.BarEnd, wantEnd)
	}
	wantPrevious := time.Date(2026, 8, 26, 16, 0, 0, 0, time.UTC)
	if !scheduled.PreviousStart.Equal(wantPrevious) {
		t.Fatalf("上一根 bar_start = %s，期望 %s", scheduled.PreviousStart, wantPrevious)
	}
	wantNext := time.Date(2026, 8, 31, 7, 0, 0, 0, time.UTC)
	if !scheduled.NextEnd.Equal(wantNext) {
		t.Fatalf("下一根 bar_end = %s，期望 %s", scheduled.NextEnd, wantNext)
	}
}

func TestAdvanceBarEndUsesTradingDays(t *testing.T) {
	friday := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	got, err := AdvanceBarEnd("cn_stock", "1d", friday, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 31, 7, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("下一个交易日 bar = %s，期望 %s", got, want)
	}
}

func TestAdvanceBarEndSkipsWeekendForTwoBarValidity(t *testing.T) {
	friday := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	got, err := AdvanceBarEnd("cn_stock", "1d", friday, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("两根 bar 的有效期 = %s，期望 %s", got, want)
	}
}
