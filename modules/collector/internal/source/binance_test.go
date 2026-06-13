package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBinanceSource_DiscoverInstruments_FiltersTradingSymbols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"symbols": [
				{"symbol":"APTUSDT","baseAsset":"APT","quoteAsset":"USDT","status":"TRADING"},
				{"symbol":"ARBTC","baseAsset":"AR","quoteAsset":"BTC","status":"TRADING"},
				{"symbol":"OLDUSDT","baseAsset":"OLD","quoteAsset":"USDT","status":"BREAK"}
			]
		}`))
	}))
	defer server.Close()

	src := NewBinanceSource(server.URL)
	instruments, err := src.DiscoverInstruments(context.Background(), DiscoverRequest{
		QuoteAsset: "USDT",
	})
	if err != nil {
		t.Fatalf("DiscoverInstruments() error = %v", err)
	}
	if len(instruments) != 1 {
		t.Fatalf("DiscoverInstruments() len = %d, want 1", len(instruments))
	}
	if instruments[0].Symbol != "APT-USDT" || instruments[0].ExternalSymbol != "APTUSDT" {
		t.Fatalf("DiscoverInstruments() = %+v, want APT-USDT/APTUSDT", instruments[0])
	}
}
