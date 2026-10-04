package paper

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

func TestMarketExecutionPriceUsesSideAndSlippage(t *testing.T) {
	quote := execution.MarketQuote{
		Bid:        shared.MustDecimal("100"),
		Ask:        shared.MustDecimal("101"),
		Last:       shared.MustDecimal("100.5"),
		SourceTime: time.UnixMilli(1_700_000_000_000),
	}
	buy, err := MarketExecutionPrice(exchange.SideBuy, quote, shared.MustDecimal("10"))
	if err != nil || buy.String() != "101.101" {
		t.Fatalf("buy price = %s, err = %v", buy, err)
	}
	sell, err := MarketExecutionPrice(exchange.SideSell, quote, shared.MustDecimal("10"))
	if err != nil || sell.String() != "99.9" {
		t.Fatalf("sell price = %s, err = %v", sell, err)
	}
}

func TestMarketExecutionPriceFallsBackToLastAndRejectsStaleQuote(t *testing.T) {
	quote := execution.MarketQuote{Last: shared.MustDecimal("10"), SourceTime: time.Unix(100, 0)}
	price, err := MarketExecutionPrice(exchange.SideBuy, quote, shared.Zero())
	if err != nil || price.String() != "10" {
		t.Fatalf("fallback price = %s, err = %v", price, err)
	}
	if QuoteFresh(quote, time.Unix(100, 0).Add(time.Second), 500*time.Millisecond) {
		t.Fatal("expired quote reported fresh")
	}
}

func TestLimitMarketableUsesLastPrice(t *testing.T) {
	if !LimitMarketable(exchange.SideBuy, shared.MustDecimal("100"), shared.MustDecimal("99")) {
		t.Fatal("buy limit should be marketable")
	}
	if LimitMarketable(exchange.SideSell, shared.MustDecimal("100"), shared.MustDecimal("99")) {
		t.Fatal("sell limit should not be marketable")
	}
}
