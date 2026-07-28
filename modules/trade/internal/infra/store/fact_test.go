package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInsertFillDeduplicatesExchangeTradeIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, seedOrder(ctx, s))

	fill := FillRecord{
		SpaceID: "space-1", FillID: "fill-1", ExchangeTradeID: "trade-1",
		OrderID: "order-1", ExchangeOrderID: "exchange-order-1",
		ExchangeAccountID: "account-1", Exchange: "BINANCE",
		MarketType: "SPOT", Symbol: "BTCUSDT", Side: "BUY",
		Price: "100", Quantity: "1", Fee: "0.1", FeeAsset: "USDT",
		SettlementAsset: "USDT", TradedAt: 123,
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		inserted, err := tx.InsertFill(fill)
		require.True(t, inserted)
		return err
	}))

	fill.FillID = "fill-2"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		inserted, err := tx.InsertFill(fill)
		require.False(t, inserted)
		return err
	}))

	var count int64
	require.NoError(t, s.db.Table("t_order_fills").Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestCreateOrderEnforcesAccountClientIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, seedOrder(ctx, s))

	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "order-2", ExchangeAccountID: "account-1",
			ClientOrderID: "client-order-1", Exchange: "BINANCE",
			MarketType: "SPOT", Symbol: "BTCUSDT", OrderType: "MARKET",
			Side: "BUY", Quantity: "1", ReferencePrice: "100",
			Source: "test", State: "READY",
		})
	})
	require.ErrorIs(t, err, ErrConflict)
}

func TestUpsertPositionUsesApprovedIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.UpsertExchangeAccount(testAccount()); err != nil {
			return err
		}
		return tx.UpsertPosition(PositionRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-1",
			Symbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
		})
	}))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertPosition(PositionRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-1",
			Symbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "-2",
		})
	}))

	var rows []struct {
		Quantity string `gorm:"column:c_signed_quantity"`
	}
	require.NoError(t, s.db.Table("t_exchange_positions").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "-2", rows[0].Quantity)
}

func seedOrder(ctx context.Context, s *Store) error {
	return s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.UpsertExchangeAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01",
			QuantityStep: "0.0001", ContractSize: "1", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "order-1", ExchangeAccountID: "account-1",
			ClientOrderID: "client-order-1", ExchangeOrderID: "exchange-order-1",
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY",
			Quantity: "1", LimitPrice: stringPointer("100"), ReferencePrice: "100",
			Source: "test", State: "OPEN", Version: 1,
		})
	})
}

func stringPointer(value string) *string { return &value }
