package eastmoney

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type jsonGetter struct {
	payload string
}

func (g jsonGetter) Get(_ context.Context, _ string, _ string, _ url.Values, result interface{}) error {
	return json.Unmarshal([]byte(g.payload), result)
}

type queryJSONGetter struct {
	payload string
	query   url.Values
}

func (g *queryJSONGetter) Get(_ context.Context, _ string, _ string, query url.Values, result interface{}) error {
	g.query = query
	return json.Unmarshal([]byte(g.payload), result)
}

func TestClientUsesProviderTimezoneForRangeDates(t *testing.T) {
	getter := &queryJSONGetter{payload: `{"rc":0,"data":{"klines":["2026-01-01,10,11,9,10.5,100,1050"]}}`}
	client := NewClient(Config{
		ProviderID: "eastmoney", SourceID: "stock_us_http", MarketID: "stock_us", InstrumentType: "equity",
		Timezone: "America/New_York", Frequencies: []string{"1d"}, AmountUnit: "usd",
		SecID: func(string) (string, error) { return "105.AAPL", nil },
	}, getter)
	_, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_us", InstrumentType: "equity", SubjectID: "US.AAPL", ProviderSymbol: "US.AAPL", Frequency: "1d", Limit: 1,
		StartTime: time.Date(2026, 1, 2, 2, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 1, 3, 2, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if getter.query.Get("beg") != "20260101" || getter.query.Get("end") != "20260102" {
		t.Fatalf("range dates = beg=%q end=%q, want New York civil dates 20260101..20260102", getter.query.Get("beg"), getter.query.Get("end"))
	}
}

func TestClientUsesConfiguredTimezoneAndCivilDayEnd(t *testing.T) {
	client := NewClient(Config{
		ProviderID: "eastmoney", SourceID: "stock_us_http", MarketID: "stock_us", InstrumentType: "equity",
		Timezone: "America/New_York", Frequencies: []string{"1d"}, VolumeUnit: "share", AmountUnit: "usd",
		SecID: func(string) (string, error) { return "105.AAPL", nil },
	}, jsonGetter{payload: `{"rc":0,"data":{"klines":["2026-03-08,10,11,9,10.5,100,1050"]}}`})

	bars, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_us", InstrumentType: "equity", SubjectID: "US.AAPL", ProviderSymbol: "US.AAPL", Frequency: "1d", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 {
		t.Fatalf("got %d bars, want 1", len(bars))
	}
	wantStart := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
	if !bars[0].BarStart.Equal(wantStart) || !bars[0].BarEnd.Equal(wantEnd) {
		t.Fatalf("bar bounds = %s..%s, want %s..%s", bars[0].BarStart, bars[0].BarEnd, wantStart, wantEnd)
	}
}

func TestBarEndUsesCalendarMonth(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, location)
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, location)
	if got := barEnd(start, "1M", location); !got.Equal(want.UTC()) {
		t.Fatalf("barEnd() = %s, want %s", got, want.UTC())
	}
}
