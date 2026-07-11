package tencent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

func TestFetchKlinesParsesUnadjustedDailyAndMinuteFixtures(t *testing.T) {
	now := time.Date(2026, 7, 13, 8, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/appstock/app/kline/get":
			_, _ = w.Write([]byte(`kline_day={"data":{"sh600000":{"day":[["2026-07-10","10.10","10.50","10.80","10.00","1000","10500","1.2"]]}}};`))
		case "/appstock/app/kline/mkline":
			_, _ = w.Write([]byte(`m5_today={"data":{"sh600000":{"m5":[["202607131000","10.5","10.6","10.7","10.4","200"]]}}};`))
		default:
			t.Fatal(r.URL.Path)
		}
	}))
	defer server.Close()
	provider := New(Config{BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return now }})
	gate := providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}
	subject := []providers.ProviderSubject{{SubjectID: "600000.XSHG", ProviderSymbol: "sh600000"}}
	daily, err := provider.FetchKlines(context.Background(), gate, providers.FetchKlinesRequest{MarketID: "stock_cn", ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, Frequency: marketdata.FrequencyDay, Subjects: subject})
	if err != nil {
		t.Fatal(err)
	}
	if len(daily.Rows) != 1 || daily.Rows[0].Close.String() != "10.5" || daily.Rows[0].Amount == nil || daily.Rows[0].Amount.String() != "10500" || !daily.Rows[0].Closed {
		t.Fatalf("daily=%+v", daily.Rows)
	}
	minute, err := provider.FetchKlines(context.Background(), gate, providers.FetchKlinesRequest{MarketID: "stock_cn", ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, Frequency: marketdata.Frequency5Min, Subjects: subject})
	if err != nil {
		t.Fatal(err)
	}
	if len(minute.Rows) != 1 || minute.Rows[0].Amount != nil || minute.Rows[0].Volume.String() != "200" {
		t.Fatalf("minute=%+v", minute.Rows)
	}
}
