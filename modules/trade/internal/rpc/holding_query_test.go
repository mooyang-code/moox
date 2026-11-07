package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func TestHoldingQuoteSymbolUsesExchangeSymbol(t *testing.T) {
	if got := holdingQuoteSymbol(exchange.Instrument{ExchangeSymbol: "BTC-USDT"}); got != "BTC-USDT" {
		t.Fatalf("quote symbol = %q, want ExchangeSymbol", got)
	}
}

func TestBalanceInstrumentIgnoresSwapInstrumentOfSameCoin(t *testing.T) {
	spot := store.InstrumentRecord{
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SPOT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
	}
	swap := store.InstrumentRecord{
		Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", InstrumentID: "BTC-USDT-SWAP",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
	}
	markets := []string{"SPOT", "SWAP"}
	// 无论现货和永续谁排在后面，余额都只对应现货标的
	for name, records := range map[string][]store.InstrumentRecord{
		"现货在前": {spot, swap},
		"永续在前": {swap, spot},
	} {
		index := holdingInstrumentIndex(records, "USDT")
		got, found := balanceInstrument(index, markets, "btc")
		if !found || got.InstrumentID != "BTC-USDT-SPOT" || got.MarketType != exchange.MarketTypeSpot {
			t.Fatalf("%s: balanceInstrument = %+v, %v，期望现货标的", name, got, found)
		}
	}
	// 只开通现货的账户，不会因为索引里有永续标的而多出匹配；计价币不是结算资产的标的不收
	if _, found := balanceInstrument(holdingInstrumentIndex([]store.InstrumentRecord{swap}, "USDT"), markets, "BTC"); found {
		t.Fatal("只有永续标的时，现货余额不应该配上永续标的")
	}
	if _, found := balanceInstrument(holdingInstrumentIndex([]store.InstrumentRecord{spot}, "USDC"), markets, "BTC"); found {
		t.Fatal("计价币不是结算资产的标的不应该被收进索引")
	}
}
