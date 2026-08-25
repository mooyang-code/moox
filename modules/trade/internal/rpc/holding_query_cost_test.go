package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func TestPaperAverageCostUsesFillBasisAndRealizedSales(t *testing.T) {
	got := paperAverageCostFromFills([]store.FillRecord{
		{Side: "SELL", Price: "120", Quantity: "2", TradedAt: 2},
		{Side: "BUY", Price: "100", Quantity: "2", TradedAt: 1},
		{Side: "BUY", Price: "110", Quantity: "2", TradedAt: 3},
	})
	if got.String() != "110" {
		t.Fatalf("average cost = %s, want 110", got.String())
	}
}
