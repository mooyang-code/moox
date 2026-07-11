package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

func TestFetchKlinesParsesConfirmedV5Candles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/market/history-candles" {
			t.Fatal(r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":"0","data":[["1783728000000","1","2","0.5","1.5","3","4","5","1"]]}`))
	}))
	defer server.Close()
	p := New(Config{BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return time.Date(2026, 7, 11, 0, 2, 0, 0, time.UTC) }})
	result, err := p.FetchKlines(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, providers.FetchKlinesRequest{MarketID: "crypto_okx", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute, Subjects: []providers.ProviderSubject{{SubjectID: "BTC-USDT", ProviderSymbol: "BTC-USDT"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Volume.String() != "3" || !result.Rows[0].Closed {
		t.Fatalf("rows=%+v", result.Rows)
	}
}
