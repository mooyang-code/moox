package consumer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedSpotBuyOrder(t *testing.T, s *store.Store, rec store.OrderRecord) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.PostLedger(rec.SpaceID, ledger.Transaction{
			ID:      shared.LedgerTransactionID("seed:deposit"),
			BizType: "seed",
			RefType: "test",
			RefID:   "deposit",
			Entries: []ledger.Entry{
				{AccountID: "exchange-clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("1000").Neg()},
				{AccountID: rec.AccountID, Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("1000")},
			},
		}); err != nil {
			return err
		}
		freeze := ledger.Transaction{
			ID:      shared.LedgerTransactionID("freeze:" + rec.OrderID),
			BizType: "freeze",
			RefType: "order",
			RefID:   rec.OrderID,
			Entries: []ledger.Entry{
				{AccountID: rec.AccountID, Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("100").Neg()},
				{AccountID: rec.AccountID, Asset: "USDT", Bucket: "frozen", Amount: shared.MustDecimal("100")},
			},
		}
		if err := tx.PostLedger(rec.SpaceID, freeze); err != nil {
			return err
		}
		return tx.CreateOrder(&rec)
	}))
}

func TestFillHandler_HandleSource_SpotBuyPartialFill_ShouldApply(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	rec := store.OrderRecord{
		SpaceID:          "space-1",
		OrderID:          "order-1",
		ClientOrderID:    "client-1",
		AccountID:        "acct-1",
		ChannelID:        "chan-1",
		Symbol:           "BTC-USDT",
		MarketType:       "spot",
		BaseAsset:        "BTC",
		QuoteAsset:       "USDT",
		Side:             "BUY",
		Quantity:         "1",
		Price:            "100",
		FilledQuantity:   "0",
		ReservedAsset:    "USDT",
		ReservedAmount:   "100",
		ConsumedReserved: "0",
		State:            string(order.Open),
		Version:          1,
	}
	seedSpotBuyOrder(t, s, rec)

	handler := FillHandler{Store: s}
	applied, err := handler.HandleSource(ctx, "space-1", "acct-1", "order-1", "fill-1", exchange.FillEvent{
		ExchangeTradeID: "trade-1",
		Symbol:          "BTC-USDT",
		Side:            "BUY",
		BaseAsset:       "BTC",
		QuoteAsset:      "USDT",
		Quantity:        shared.MustDecimal("0.5"),
		Price:           shared.MustDecimal("100"),
		Fee:             shared.Zero(),
	}, "test")
	require.NoError(t, err)
	assert.True(t, applied)

	got, err := s.GetOrder(ctx, "space-1", "order-1")
	require.NoError(t, err)
	assert.Equal(t, "0.5", got.FilledQuantity)
	assert.Equal(t, string(order.PartiallyFilled), got.State)
}

func TestFillHandler_HandleSource_DuplicateFill_ShouldReturnFalse(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	rec := store.OrderRecord{
		SpaceID:          "space-1",
		OrderID:          "order-1",
		ClientOrderID:    "client-1",
		AccountID:        "acct-1",
		ChannelID:        "chan-1",
		Symbol:           "BTC-USDT",
		MarketType:       "spot",
		BaseAsset:        "BTC",
		QuoteAsset:       "USDT",
		Side:             "BUY",
		Quantity:         "1",
		Price:            "100",
		FilledQuantity:   "0",
		ReservedAsset:    "USDT",
		ReservedAmount:   "100",
		ConsumedReserved: "0",
		State:            string(order.Open),
		Version:          1,
	}
	seedSpotBuyOrder(t, s, rec)

	handler := FillHandler{Store: s}
	event := exchange.FillEvent{
		ExchangeTradeID: "trade-1",
		Symbol:          "BTC-USDT",
		Side:            "BUY",
		BaseAsset:       "BTC",
		QuoteAsset:      "USDT",
		Quantity:        shared.MustDecimal("0.5"),
		Price:           shared.MustDecimal("100"),
		Fee:             shared.Zero(),
	}
	applied1, err := handler.HandleSource(ctx, "space-1", "acct-1", "order-1", "fill-1", event, "test")
	require.NoError(t, err)
	assert.True(t, applied1)

	applied2, err := handler.HandleSource(ctx, "space-1", "acct-1", "order-1", "fill-1", event, "test")
	require.NoError(t, err)
	assert.False(t, applied2)
}

func TestFillHandler_HandleSource_FullFill_ShouldMarkFilled(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	rec := store.OrderRecord{
		SpaceID:          "space-1",
		OrderID:          "order-2",
		ClientOrderID:    "client-2",
		AccountID:        "acct-1",
		ChannelID:        "chan-1",
		Symbol:           "BTC-USDT",
		MarketType:       "spot",
		BaseAsset:        "BTC",
		QuoteAsset:       "USDT",
		Side:             "BUY",
		Quantity:         "1",
		Price:            "100",
		FilledQuantity:   "0",
		ReservedAsset:    "USDT",
		ReservedAmount:   "100",
		ConsumedReserved: "0",
		State:            string(order.Open),
		Version:          1,
	}
	seedSpotBuyOrder(t, s, rec)

	handler := FillHandler{Store: s}
	applied, err := handler.HandleSource(ctx, "space-1", "acct-1", "order-2", "fill-2", exchange.FillEvent{
		ExchangeTradeID: "trade-2",
		Symbol:          "BTC-USDT",
		Side:            "BUY",
		BaseAsset:       "BTC",
		QuoteAsset:      "USDT",
		Quantity:        shared.MustDecimal("1"),
		Price:           shared.MustDecimal("100"),
		Fee:             shared.Zero(),
	}, "test")
	require.NoError(t, err)
	assert.True(t, applied)

	got, err := s.GetOrder(ctx, "space-1", "order-2")
	require.NoError(t, err)
	assert.Equal(t, string(order.Filled), got.State)
	assert.Equal(t, "1", got.FilledQuantity)
}

func TestFillHandler_Handle_IncompleteFill_ShouldReturnError(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	rec := store.OrderRecord{
		SpaceID:        "space-1",
		OrderID:        "order-1",
		ClientOrderID:  "client-1",
		AccountID:      "acct-1",
		ChannelID:      "chan-1",
		Symbol:         "BTC-USDT",
		Quantity:       "1",
		FilledQuantity: "0",
		State:          string(order.Open),
		Version:        1,
	}
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(&rec)
	}))

	handler := FillHandler{Store: s}
	err = handler.Handle(ctx, "space-1", "acct-1", "order-1", "fill-1", exchange.FillEvent{
		ExchangeTradeID: "trade-1",
		Symbol:          "BTC-USDT",
		Quantity:        shared.MustDecimal("0.5"),
		Price:           shared.MustDecimal("100"),
		Fee:             shared.Zero(),
	})
	assert.Error(t, err)
}
