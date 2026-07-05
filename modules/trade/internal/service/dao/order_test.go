package dao

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
)

func TestUpsertOrdersUpdatesExistingClientOrderID(t *testing.T) {
	store := newSyncCursorTestStore(t)
	ctx := context.Background()
	local := &service.Order{
		OrderID:       "local_order",
		ClientOrderID: "client_1",
		AccountID:     "acc_1",
		ChannelID:     "ch_1",
		Exchange:      "binance",
		Symbol:        "BTCUSDT",
		MarketType:    "spot",
		Side:          "buy",
		OrderType:     "market",
		Status:        1,
	}
	if err := store.SaveOrder(ctx, "crypto", local); err != nil {
		t.Fatalf("SaveOrder returned error: %v", err)
	}
	synced := &service.Order{
		OrderID:         "deterministic_order",
		ClientOrderID:   "client_1",
		ExchangeOrderID: "exchange_1",
		AccountID:       "acc_1",
		ChannelID:       "ch_1",
		Exchange:        "binance",
		Symbol:          "BTCUSDT",
		MarketType:      "spot",
		Side:            "buy",
		OrderType:       "market",
		Status:          3,
		FilledQty:       "1",
	}
	if err := store.UpsertOrders(ctx, "crypto", []*service.Order{synced}); err != nil {
		t.Fatalf("UpsertOrders returned error: %v", err)
	}
	got, err := store.GetOrder(ctx, "crypto", "local_order", "")
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if got.OrderID != "local_order" || got.ExchangeOrderID != "exchange_1" || got.Status != 3 || got.FilledQty != "1" {
		t.Fatalf("order = %+v, want local id preserved and exchange fields updated", got)
	}
}
