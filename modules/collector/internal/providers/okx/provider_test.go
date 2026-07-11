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

func TestFetchInstrumentsNormalizesSpotUniverse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/public/instruments" || r.URL.Query().Get("instType") != "SPOT" {
			t.Fatal(r.URL.String())
		}
		_, _ = w.Write([]byte(`{"code":"0","data":[{"instId":"BTC-USDT","baseCcy":"BTC","quoteCcy":"USDT","state":"live","listTime":"1783728000000","expTime":""}]}`))
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	p := New(Config{BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return now }})
	result, err := p.FetchInstruments(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, providers.FetchInstrumentsRequest{MarketID: "crypto_okx", ExchangeID: "OKX", SnapshotAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instruments) != 1 || result.Instruments[0].ProviderSymbol != "BTC-USDT" || result.Instruments[0].Currency != "USDT" || !result.Complete {
		t.Fatalf("result=%+v", result)
	}
}

func TestFetchSwapInstrumentsAndCandlesPreserveContractUnits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/public/instruments":
			if r.URL.Query().Get("instType") != "SWAP" {
				t.Fatal(r.URL.String())
			}
			_, _ = w.Write([]byte(`{"code":"0","data":[{"instId":"BTC-USDT-SWAP","ctValCcy":"BTC","settleCcy":"USDT","state":"live","listTime":"1783728000000"}]}`))
		case "/api/v5/market/history-candles":
			_, _ = w.Write([]byte(`{"code":"0","data":[["1783728000000","1","2","0.5","1.5","3","4","5","1"]]}`))
		default:
			t.Fatal(r.URL.Path)
		}
	}))
	defer server.Close()
	now := time.Date(2026, 7, 11, 0, 2, 0, 0, time.UTC)
	p := New(Config{BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return now }})
	instruments, err := p.FetchInstruments(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, providers.FetchInstrumentsRequest{InstrumentTypes: []marketdata.InstrumentType{marketdata.InstrumentSwap}, SnapshotAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments.Instruments) != 1 || instruments.Instruments[0].ProductType != marketdata.ProductSwap || instruments.Instruments[0].Currency != "USDT" {
		t.Fatalf("instruments=%+v", instruments)
	}
	klines, err := p.FetchKlines(context.Background(), providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, providers.FetchKlinesRequest{ProductType: marketdata.ProductSwap, InstrumentType: marketdata.InstrumentSwap, Frequency: marketdata.FrequencyMinute, Subjects: []providers.ProviderSubject{{SubjectID: "BTC-USDT-SWAP", ProviderSymbol: "BTC-USDT-SWAP"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(klines.Rows) != 1 || klines.Rows[0].FeedScope != "swap" || klines.Rows[0].VolumeUnit != "contract" {
		t.Fatalf("klines=%+v", klines.Rows)
	}
}
