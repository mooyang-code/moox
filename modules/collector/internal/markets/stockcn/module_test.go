package stockcn

import (
	"testing"
	"time"
)

func TestCanonicalSubjectAndProviderSymbols(t *testing.T) {
	for _, tc := range []struct{ code, subject, ifeng string }{{"600000", "600000.XSHG", "sh600000"}, {"000001", "000001.XSHE", "sz000001"}, {"830799", "830799.XBSE", "bj830799"}} {
		subject, err := CanonicalSubject(tc.code)
		if err != nil || subject != tc.subject {
			t.Fatalf("%s => %s %v", tc.code, subject, err)
		}
		symbol, err := ProviderSymbol("ifeng", subject)
		if err != nil || symbol != tc.ifeng {
			t.Fatalf("symbol=%s err=%v", symbol, err)
		}
	}
}
func TestCalendarHonorsMiddayHolidayAndHorizon(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stock_cn/calendar.yaml")
	if err != nil {
		t.Fatal(err)
	}
	holiday := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	if calendar.IsOpen(holiday) {
		t.Fatal("holiday reported open")
	}
	morning := time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC)
	if !calendar.IsOpen(morning) {
		t.Fatal("regular morning session closed")
	}
	midday := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
	if calendar.IsOpen(midday) {
		t.Fatal("midday break open")
	}
}

func TestCalendarTradingDaysMaterializesTwoSessionsAndSkipsHoliday(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stock_cn/calendar.yaml")
	if err != nil {
		t.Fatal(err)
	}
	location, _ := time.LoadLocation("Asia/Shanghai")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 3)
	days, err := calendar.TradingDays(start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].TradeDate != "2026-01-02" || len(days[0].Sessions) != 2 {
		t.Fatalf("days=%+v", days)
	}
	if got := days[0].Sessions[0].Open.In(location).Format("15:04"); got != "09:30" {
		t.Fatalf("first open=%s", got)
	}
}
