package paper

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestRebuildSpotStateIncludesFeesAndReservations(t *testing.T) {
	account := tradingaccount.Account{
		ExecutionMode:   exchange.ExecutionModePaper,
		SettlementAsset: "USDT",
		Paper: &tradingaccount.PaperConfig{
			InitialBalance: shared.MustDecimal("1000"),
		},
	}
	instruments := []exchange.Instrument{{
		ExchangeSymbol: "BTCUSDT", QuoteAsset: "USDT",
	}}
	fee := shared.MustDecimal("0.4")
	state, err := Rebuild(account, instruments, []exchange.Fill{{
		ExchangeSymbol: "BTCUSDT",
		Side:           exchange.SideBuy, Quantity: shared.MustDecimal("2"), Price: shared.MustDecimal("100"),
		Fee: fee, FeeAsset: "USDT", TradedAt: time.UnixMilli(1),
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Balances["USDT"].String(); got != "799.6" {
		t.Fatalf("USDT balance = %s, want 799.6", got)
	}
	if got := state.Positions["BTCUSDT"].Quantity.String(); got != "2" {
		t.Fatalf("position quantity = %s, want 2", got)
	}
	if state.CumulativeFee.Cmp(fee) != 0 {
		t.Fatalf("cumulative fee = %s, want %s", state.CumulativeFee, fee)
	}
}
