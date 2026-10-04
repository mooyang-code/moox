package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestHoldingQuoteSymbolUsesExchangeSymbol(t *testing.T) {
	if got := holdingQuoteSymbol(exchange.Instrument{ExchangeSymbol: "BTC-USDT"}); got != "BTC-USDT" {
		t.Fatalf("quote symbol = %q, want ExchangeSymbol", got)
	}
}
