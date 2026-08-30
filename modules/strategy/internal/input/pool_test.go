package input

import (
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

func TestBuildPoolFiltersHistoryAndChoosesPreferredVenue(t *testing.T) {
	rule := config.InstrumentPoolRule{Exchanges: []string{"binance", "okx"}, Markets: []string{"spot"}, QuoteAssets: []string{"USDT"}, MinHistoryPeriods: 2}
	got := BuildPool(rule, []Subject{
		{SubjectID: "btc-binance", InstrumentID: "BTC-USDT-SPOT", Exchange: "binance", Market: "spot", QuoteAsset: "USDT", Active: true},
		{SubjectID: "btc-okx", InstrumentID: "BTC-USDT-SPOT", Exchange: "okx", Market: "spot", QuoteAsset: "USDT", Active: true},
		{SubjectID: "eth", InstrumentID: "ETH-USDT-SPOT", Exchange: "binance", Market: "spot", QuoteAsset: "USDT", Active: true},
	}, map[string]int{"BTC-USDT-SPOT": 2, "ETH-USDT-SPOT": 1})
	if len(got.Items) != 1 || got.Items[0].SubjectID != "btc-binance" || got.Ineligible["ETH-USDT-SPOT"] != "insufficient_history" {
		t.Fatalf("unexpected pool: %+v", got)
	}
}

func TestBuildPoolFallsBackToNextVenueWhenPreferredHasNoHistory(t *testing.T) {
	rule := config.InstrumentPoolRule{Exchanges: []string{"binance", "okx"}, MinHistoryPeriods: 2}
	got := BuildPool(rule, []Subject{
		{SubjectID: "btc-binance", InstrumentID: "BTC-USDT-SPOT", Exchange: "binance", Active: true},
		{SubjectID: "btc-okx", InstrumentID: "BTC-USDT-SPOT", Exchange: "okx", Active: true},
	}, map[string]int{"btc-okx": 2})
	if len(got.Items) != 1 || got.Items[0].SubjectID != "btc-okx" {
		t.Fatalf("expected eligible fallback venue, got %+v", got)
	}
}

func TestEvaluationInputHashIsStableAcrossItemOrder(t *testing.T) {
	a := InstrumentInput{PoolItem: PoolItem{InstrumentID: "A"}, Values: map[string]quant.Decimal{"x": quant.Must("1")}}
	b := InstrumentInput{PoolItem: PoolItem{InstrumentID: "B"}, Values: map[string]quant.Decimal{"x": quant.Must("2")}}
	left := EvaluationInput{SpaceID: "s", StrategyID: "st", PeriodEnd: "p", Items: []InstrumentInput{a, b}}
	right := EvaluationInput{SpaceID: "s", StrategyID: "st", PeriodEnd: "p", Items: []InstrumentInput{b, a}}
	h1, err := Hash(left)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := Hash(right)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash changed with order: %s != %s", h1, h2)
	}
}
