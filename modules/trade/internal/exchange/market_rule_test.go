package exchange

import (
	"errors"
	"testing"
)

func TestRuleForDescribesEachMarket(t *testing.T) {
	spot, swap, stock := RuleFor(MarketTypeSpot), RuleFor(MarketTypeSwap), RuleFor("STOCK")
	if !spot.PositionFromBalance || spot.CanShort || spot.UsesMargin || spot.RawInstrumentID {
		t.Fatalf("SPOT rule = %+v", spot)
	}
	if swap.PositionFromBalance || !swap.CanShort || !swap.ReduceOnly || !swap.Leverage ||
		!swap.ContractValue || swap.PositionSide != PositionSideNet {
		t.Fatalf("SWAP rule = %+v", swap)
	}
	if !stock.PositionFromBalance || stock.CanShort || !stock.RawInstrumentID {
		t.Fatalf("STOCK rule = %+v", stock)
	}
	if RuleFor("UNKNOWN") != (MarketRule{}) {
		t.Fatal("未知市场应返回零值")
	}
}

func TestNormalizeInstrumentIDKeepsStockIDs(t *testing.T) {
	got, err := NormalizeInstrumentID(" 600519.xshg ", MarketTypeStock)
	if err != nil || got != "600519.XSHG" {
		t.Fatalf("NormalizeInstrumentID() = %q, %v", got, err)
	}
	if _, err := NormalizeInstrumentID("  ", MarketTypeStock); !errors.Is(err, ErrInvalidInstrumentID) {
		t.Fatalf("空标的 error = %v", err)
	}
}
