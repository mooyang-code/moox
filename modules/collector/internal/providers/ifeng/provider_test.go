package ifeng

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

func TestFetchKlinesParsesDailyRecordWithoutAdjustment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/akdaily/" || r.URL.Query().Get("type") != "last" {
			t.Fatalf("request=%s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"record":[["2026-07-10","10.10","11.00","10.80","9.90","12345","0","0","0","0","0","0","0","0","0"]]}`))
	}))
	defer server.Close()
	p := New(Config{BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC) }})
	result, err := p.FetchKlines(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, providers.FetchKlinesRequest{MarketID: "stock_cn", ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, Frequency: marketdata.FrequencyDay, Subjects: []providers.ProviderSubject{{SubjectID: "600000.XSHG", ProviderSymbol: "sh600000"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || result.Rows[0].Close.String() != "10.8" || result.Rows[0].Amount != nil || !result.Rows[0].Closed {
		t.Fatalf("rows=%+v", result.Rows)
	}
}
