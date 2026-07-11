package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

func TestFetchKlinesNormalizesClosedSpotRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/klines" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[[1783728000000,"1.10","1.30","1.00","1.20","2.50",1783728059999,"3.00",12,"0","0","0"]]`))
	}))
	defer server.Close()
	p := New(Config{BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return time.Date(2026, 7, 11, 0, 2, 0, 0, time.UTC) }})
	result, err := p.FetchKlines(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, providers.FetchKlinesRequest{MarketID: "crypto_binance", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyMinute, Subjects: []providers.ProviderSubject{{SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT"}}, StartTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 7, 11, 0, 1, 0, 0, time.UTC), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Close.String() != "1.2" || result.Rows[0].Amount.String() != "3" || !result.Rows[0].Closed {
		t.Fatalf("rows=%+v", result.Rows)
	}
}
