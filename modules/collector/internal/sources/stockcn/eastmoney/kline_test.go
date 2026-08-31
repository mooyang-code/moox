package eastmoney

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type fakeGetter struct {
	payload response
	query   url.Values
}

func (f *fakeGetter) Get(_ context.Context, _ string, _ string, query url.Values, result interface{}) error {
	f.query = query
	*result.(*response) = f.payload
	return nil
}

func TestSecIDRequiresExplicitExchange(t *testing.T) {
	if got, err := SecID("SH.600000"); err != nil || got != "1.600000" {
		t.Fatalf("SecID() = %q, %v", got, err)
	}
	if _, err := SecID("600000"); err == nil {
		t.Fatal("bare symbol should not be guessed")
	}
}

func TestFetchKlinesParsesEastMoneyFieldsAndStartLabels(t *testing.T) {
	getter := &fakeGetter{payload: response{RC: 0, Data: &struct {
		Klines []string `json:"klines"`
	}{Klines: []string{"2026-08-31 09:30,10,10.5,11,9.5,100,1050,15,1,0.5,2"}}}}
	client := NewClient(getter)
	client.Now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }
	bars, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{MarketID: "stock_cn", InstrumentType: "equity", SubjectID: "600000", ProviderSymbol: "SH.600000", Frequency: "1m", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Open.String() != "10" || bars[0].High.String() != "11" || bars[0].Amount.Value.String() != "1050" {
		t.Fatalf("unexpected bars: %+v", bars)
	}
	if getter.query.Get("klt") != "1" || getter.query.Get("fqt") != "0" {
		t.Fatalf("unexpected query: %v", getter.query)
	}
}

func TestFetchKlinesUsesCalendarMonthForMonthlyBars(t *testing.T) {
	getter := &fakeGetter{payload: response{RC: 0, Data: &struct {
		Klines []string `json:"klines"`
	}{Klines: []string{"2026-01-31,10,10.5,11,9.5,100,1050"}}}}
	client := NewClient(getter)
	bars, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_cn", InstrumentType: "equity", SubjectID: "SH.600000", ProviderSymbol: "SH.600000", Frequency: "1M", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 {
		t.Fatalf("got %d bars, want 1", len(bars))
	}
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)).UTC()
	if !bars[0].BarEnd.Equal(want) {
		t.Fatalf("monthly bar end = %s, want %s", bars[0].BarEnd, want)
	}
}
