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
	fetch := func(context.Context) (map[string]shared.Decimal, error) {
		calls++
		return map[string]shared.Decimal{"BTCUSDT": shared.MustDecimal("100")}, nil
	}
	for range 3 {
		if price, _, ok := board.Get(context.Background(), "BTCUSDT", fetch); !ok || price.String() != "100" {
			t.Fatalf("price = %s, ok = %v", price, ok)
		}
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d", calls)
	}
	if _, _, ok := board.Get(context.Background(), "ETHUSDT", fetch); ok {
		t.Fatal("没有的标的应退回单标的接口")
	}
}

func TestPriceBoardFetchFailureFallsBack(t *testing.T) {
	board := &PriceBoard{}
	_, _, ok := board.Get(context.Background(), "BTCUSDT", func(context.Context) (map[string]shared.Decimal, error) {
		return nil, errors.New("boom")
	})
	if ok {
		t.Fatal("失败时应退回单标的接口")
	}
}
