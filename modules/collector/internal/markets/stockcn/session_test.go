package stockcn

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

func TestChinaStockSessionSeparatesLunchAndHoliday(t *testing.T) {
	calendar, err := marketcalendar.Load("cn_stock")
	if err != nil {
		t.Fatal(err)
	}
	policy := TradabilityPolicy{Calendar: calendar, Session: ChinaStockSession()}
	location := policy.Session.Location
	for at, want := range map[time.Time]TradabilityStatus{
		time.Date(2026, 8, 31, 10, 0, 0, 0, location): Tradable,
		time.Date(2026, 8, 31, 12, 0, 0, 0, location): OutsideSession,
		time.Date(2026, 8, 30, 10, 0, 0, 0, location): NonTradingDayStatus,
	} {
		got, err := policy.Status(at)
		if err != nil || got != want {
			t.Fatalf("Status(%s) = %s, %v; want %s", at, got, err, want)
		}
	}
}

func TestExpectedMinuteBucketsIncludeTwoSessions(t *testing.T) {
	calendar, err := marketcalendar.Load("cn_stock")
	if err != nil {
		t.Fatal(err)
	}
	date := marketcalendar.MustParseCivilDate("2026-08-31")
	buckets, err := (TradabilityPolicy{Calendar: calendar, Session: ChinaStockSession()}).ExpectedMinuteBuckets(date)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 240 || buckets[119].Hour() != 11 || buckets[120].Hour() != 13 {
		t.Fatalf("unexpected bucket boundaries: len=%d first=%s lunch=%s", len(buckets), buckets[0], buckets[120])
	}
}
