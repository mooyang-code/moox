package crypto

import (
	"testing"
	"time"
)

func TestCalendarMaterializesEveryUTCDate(t *testing.T) {
	start := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	days, err := (CalendarPolicy{TwentyFourSeven: true, ExchangeID: "BINANCE"}).TradingDays(start, start.Add(36*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].TradeDate != "2026-07-11" || days[1].TradeDate != "2026-07-12" || len(days[0].Sessions) != 1 {
		t.Fatalf("days=%+v", days)
	}
}
