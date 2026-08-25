package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestHoldingQuoteSymbolFallsBackToLegacySymbol(t *testing.T) {
	if got := holdingQuoteSymbol(exchange.Instrument{ExchangeSymbol: "BTC-USDT", Symbol: "BTCUSDT"}); got != "BTC-USDT" {
		t.Fatalf("quote symbol = %q, want ExchangeSymbol", got)
	}
	if got := holdingQuoteSymbol(exchange.Instrument{Symbol: "BTCUSDT"}); got != "BTCUSDT" {
		t.Fatalf("quote symbol = %q, want Symbol fallback", got)
	}
}
