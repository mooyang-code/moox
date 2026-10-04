package input

import (
	"testing"
	"time"
)

func TestClosedBarCryptoUsesMostRecentClosedBoundary(t *testing.T) {
	trigger := time.Date(2026, 8, 29, 11, 7, 30, 0, time.UTC)
	got, err := ClosedBar("crypto_24x7", "1H", trigger, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 29, 11, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("closed bar = %s, want %s", got, want)
	}
}

func TestClosedBarStockUsesTradingDayClose(t *testing.T) {
	trigger := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	days := []time.Time{time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)}
	got, err := ClosedBar("cn_stock", "1d", trigger, days)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("closed bar = %s, want %s", got, want)
	}
}

func TestStockPeriodSkipsWeekendAndKeepsEventScheduleIdentity(t *testing.T) {
	// Monday before the close still evaluates Friday; the event carrying
	// Friday's midnight Storage key must resolve to the same close boundary.
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
		t.Fatalf("scheduled/event period mismatch: scheduled=%+v event=%+v", scheduled, event)
	}
	wantEnd := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	if !scheduled.BarEnd.Equal(wantEnd) {
		t.Fatalf("bar end = %s, want %s", scheduled.BarEnd, wantEnd)
	}
	wantPrevious := time.Date(2026, 8, 26, 16, 0, 0, 0, time.UTC)
	if !scheduled.PreviousStart.Equal(wantPrevious) {
		t.Fatalf("previous storage start = %s, want %s", scheduled.PreviousStart, wantPrevious)
	}
	wantNext := time.Date(2026, 8, 31, 7, 0, 0, 0, time.UTC)
	if !scheduled.NextEnd.Equal(wantNext) {
		t.Fatalf("next bar end = %s, want %s", scheduled.NextEnd, wantNext)
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
		t.Fatalf("next trading bar = %s, want %s", got, want)
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
		t.Fatalf("two-bar validity = %s, want %s", got, want)
	}
}
