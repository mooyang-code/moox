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

func TestExchangeInstrumentSwapFieldsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	want := InstrumentRecord{
		Exchange: "OKX", MarketType: "SWAP", Symbol: "BTC-USDT-SWAP",
		InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
		SettlementAsset: "USDT", Linear: true, ContractValue: "0.01",
		ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
		MinExchangeQuantity: "1", PriceTick: "0.1", MinNotional: "10",
		Status: "TRADING", ExchangeUpdatedAt: 123,
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertInstrument(want)
	}))

	got, err := s.GetInstrument(ctx, want.Exchange, want.MarketType, want.Symbol)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestExchangeInstrumentRejectsIncompleteSwapConversion(t *testing.T) {
	s := openTestStore(t)
	err := s.Transaction(context.Background(), func(tx *Tx) error {
		return tx.UpsertInstrument(InstrumentRecord{
			Exchange: "OKX", MarketType: "SWAP", Symbol: "BTC-USDT-SWAP",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ExchangeQuantityStep: "1", PriceTick: "0.1", Status: "TRADING",
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)

	err = s.db.Exec(`
		INSERT INTO t_exchange_instruments (
			c_exchange, c_market_type, c_symbol, c_base_asset, c_quote_asset,
			c_settlement_asset, c_linear, c_contract_value, c_contract_value_asset,
			c_exchange_quantity_step, c_min_exchange_quantity, c_price_tick, c_status
		) VALUES ('OKX', 'SWAP', 'ETH-USDT-SWAP', 'ETH', 'USDT', 'USDT', 0,
			'0', '', '1', '0', '0.01', 'TRADING')
	`).Error
	require.Error(t, err)
}

func TestCreateOrderRejectsContradictoryExchangeIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.UpsertExchangeAccount(testAccount()); err != nil {
			return err
		}
		return tx.UpsertInstrument(InstrumentRecord{
			Exchange: "OKX", MarketType: "SWAP", Symbol: "BTC-USDT-SWAP",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			Linear: true, ContractValue: "0.01", ContractValueAsset: "BTC",
			ExchangeQuantityStep: "1", MinExchangeQuantity: "1",
			PriceTick: "0.1", Status: "TRADING",
		})
	}))

	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "order-wrong", ExchangeAccountID: "account-1",
			ClientOrderID: "client-wrong", Exchange: "OKX", MarketType: "SWAP",
			Symbol: "BTC-USDT-SWAP", OrderType: "MARKET", Side: "BUY",
			Quantity: "1", ReferencePrice: "100", Source: "test", State: "READY",
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

func TestCreateOrderDerivesExchangeIdentityFromAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.UpsertExchangeAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", ExchangeQuantityStep: "0.0001",
			PriceTick: "0.01", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "derived-order",
			ExchangeAccountID: "account-1", ClientOrderID: "derived-client",
			Symbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
			Quantity: "1", ReferencePrice: "100", Source: "test", State: "READY",
		})
	}))

	var identity struct {
		Exchange   string `gorm:"column:c_exchange"`
		MarketType string `gorm:"column:c_market_type"`
	}
	require.NoError(t, s.db.Table("t_trade_orders").
		Select("c_exchange, c_market_type").
		Where("c_space_id = ? AND c_order_id = ?", "space-1", "derived-order").
		Take(&identity).Error)
	require.Equal(t, "BINANCE", identity.Exchange)
	require.Equal(t, "SPOT", identity.MarketType)
}

func TestInsertFillRejectsContradictoryOrderIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, seedOrder(ctx, s))
	base := FillRecord{
		SpaceID: "space-1", FillID: "fill-wrong", ExchangeTradeID: "trade-wrong",
		OrderID: "order-1", ExchangeAccountID: "other-account",
		Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
		Side: "BUY", Price: "100", Quantity: "1",
	}
	err := s.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.InsertFill(base)
		return err
	})
	require.ErrorIs(t, err, ErrInvalidRecord)

	base.ExchangeAccountID = "account-1"
	base.MarketType = "SWAP"
	err = s.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.InsertFill(base)
		return err
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
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
			ExchangeQuantityStep: "0.0001", Status: "TRADING",
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
