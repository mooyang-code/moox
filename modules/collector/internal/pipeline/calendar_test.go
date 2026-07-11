package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

func TestCalendarPipelineMaterializesBoundedPolicyPages(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	store := &calendarStoreFake{}
	pipe := CalendarPipeline{Policy: calendarPolicyFake{days: []markets.CalendarDay{{ExchangeID: "BINANCE", TradeDate: "2026-07-11", Timezone: "UTC", Status: "open", Sessions: []markets.CalendarSession{{Open: base, Close: base.Add(24 * time.Hour)}}}, {ExchangeID: "BINANCE", TradeDate: "2026-07-12", Timezone: "UTC", Status: "open", Sessions: []markets.CalendarSession{{Open: base.Add(24 * time.Hour), Close: base.Add(48 * time.Hour)}}}}}, Store: store, DatasetID: "calendar", Generation: base}
	first, err := pipe.Materialize(context.Background(), CalendarRequest{Start: base, End: base.Add(48 * time.Hour), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Rows != 1 || first.Complete || first.NextCursor != "1" || len(store.days) != 1 {
		t.Fatalf("first=%+v days=%+v", first, store.days)
	}
	second, err := pipe.Materialize(context.Background(), CalendarRequest{Start: base, End: base.Add(48 * time.Hour), Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if second.Rows != 1 || !second.Complete || len(store.days) != 2 {
		t.Fatalf("second=%+v days=%+v", second, store.days)
	}
}

type calendarPolicyFake struct{ days []markets.CalendarDay }

func (p calendarPolicyFake) TradingDays(time.Time, time.Time) ([]markets.CalendarDay, error) {
	return append([]markets.CalendarDay(nil), p.days...), nil
}

type calendarStoreFake struct{ days []markets.CalendarDay }

func (s *calendarStoreFake) WriteCalendarDays(_ context.Context, _ string, _ time.Time, days []markets.CalendarDay) error {
	s.days = append(s.days, days...)
	return nil
}
