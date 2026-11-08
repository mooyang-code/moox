package exchange

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

func TestPriceBoardReusesOneFetch(t *testing.T) {
	board := &PriceBoard{}
	calls := 0
	fetch := func(context.Context) (map[SymbolKey]shared.Decimal, error) {
		calls++
		return map[SymbolKey]shared.Decimal{{Market: MarketTypeSwap, Symbol: "BTCUSDT"}: shared.MustDecimal("100")}, nil
	}
	for range 3 {
		if price, _, ok := board.Get(context.Background(), SymbolKey{Market: MarketTypeSwap, Symbol: "BTCUSDT"}, fetch); !ok || price.String() != "100" {
			t.Fatalf("price = %s, ok = %v", price, ok)
		}
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d", calls)
	}
	if _, _, ok := board.Get(context.Background(), SymbolKey{Market: MarketTypeSwap, Symbol: "ETHUSDT"}, fetch); ok {
		t.Fatal("没有的标的应退回单标的接口")
	}
}

func TestPriceBoardFetchFailureFallsBack(t *testing.T) {
	board := &PriceBoard{}
	_, _, ok := board.Get(context.Background(), SymbolKey{Market: MarketTypeSwap, Symbol: "BTCUSDT"}, func(context.Context) (map[SymbolKey]shared.Decimal, error) {
		return nil, errors.New("boom")
	})
	if ok {
		t.Fatal("失败时应退回单标的接口")
	}
}

func TestPriceBoardKeepsSpotAndSwapSeparate(t *testing.T) {
	board := &PriceBoard{}
	spot := SymbolKey{Market: MarketTypeSpot, Symbol: "BTCUSDT"}
	swap := SymbolKey{Market: MarketTypeSwap, Symbol: "BTCUSDT"}
	fetch := func(context.Context) (map[SymbolKey]shared.Decimal, error) {
		return map[SymbolKey]shared.Decimal{spot: shared.MustDecimal("100"), swap: shared.MustDecimal("101")}, nil
	}
	if price, _, ok := board.Get(context.Background(), spot, fetch); !ok || price.String() != "100" {
		t.Fatalf("现货价 = %s, ok = %v", price, ok)
	}
	if price, _, ok := board.Get(context.Background(), swap, fetch); !ok || price.String() != "101" {
		t.Fatalf("合约价 = %s, ok = %v", price, ok)
	}
}
