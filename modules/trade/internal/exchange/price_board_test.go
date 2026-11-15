package exchange

import (
	"context"
	"errors"
	"testing"
	"time"

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

// 只读缓存的上下文：不调全市场接口，只用已有的快照；快照没有或太旧都按取不到处理。
func TestPriceBoardCachedOnlyNeverFetches(t *testing.T) {
	board := &PriceBoard{}
	key := SymbolKey{Market: MarketTypeSwap, Symbol: "BTCUSDT"}
	calls := 0
	fetch := func(context.Context) (map[SymbolKey]shared.Decimal, error) {
		calls++
		return map[SymbolKey]shared.Decimal{key: shared.MustDecimal("100")}, nil
	}
	cached := WithCachedQuotes(context.Background(), time.Minute)
	if _, _, ok := board.Get(cached, key, fetch); ok || calls != 0 {
		t.Fatalf("没有快照时应取不到且不发请求：ok = %v, calls = %d", ok, calls)
	}
	if _, _, ok := board.Get(context.Background(), key, fetch); !ok || calls != 1 {
		t.Fatalf("普通取价应取一次：ok = %v, calls = %d", ok, calls)
	}
	// 快照已经过了 2 秒的复用时间也照用，只要不比 maxAge 旧。
	time.Sleep(5 * time.Millisecond)
	if price, _, ok := board.Get(cached, key, fetch); !ok || price.String() != "100" || calls != 1 {
		t.Fatalf("应读到已有快照：price = %s, ok = %v, calls = %d", price, ok, calls)
	}
	if _, _, ok := board.Get(WithCachedQuotes(context.Background(), time.Millisecond), key, fetch); ok || calls != 1 {
		t.Fatalf("比 maxAge 旧的快照不用：ok = %v, calls = %d", ok, calls)
	}
}
