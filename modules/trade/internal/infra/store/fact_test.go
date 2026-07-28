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

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		inserted, err := tx.InsertFill(fill)
		require.False(t, inserted)
		return err
	}))

	fill.FillID = "fill-2"
	fill.Price = "101"
	err := s.Transaction(ctx, func(tx *Tx) error {
		inserted, err := tx.InsertFill(fill)
		require.False(t, inserted)
		return err
	})
	require.ErrorIs(t, err, ErrConflict)

	fill.FillID = "fill-1"
	fill.Price = "100"
	fill.Side = "SELL"
	err = s.Transaction(ctx, func(tx *Tx) error {
		inserted, err := tx.InsertFill(fill)
		require.False(t, inserted)
		return err
	})
	require.ErrorIs(t, err, ErrConflict)

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "order-2", ExchangeAccountID: "account-1",
			ClientOrderID: "client-order-2", ExchangeOrderID: "exchange-order-2",
			Symbol: "BTCUSDT", OrderType: "LIMIT", TimeInForce: "GTC",
			Side: "BUY", Quantity: "1", LimitPrice: stringPointer("100"),
			ReferencePrice: "100", Source: "test", State: "OPEN",
		})
	}))
	fill.FillID = "fill-2"
	fill.OrderID = "order-2"
	fill.ExchangeOrderID = "exchange-order-2"
	fill.Side = "BUY"
	err = s.Transaction(ctx, func(tx *Tx) error {
		inserted, err := tx.InsertFill(fill)
		require.False(t, inserted)
		return err
	})
	require.ErrorIs(t, err, ErrConflict)

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
		if err := tx.CreateExchangeAccount(testAccount()); err != nil {
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
		if err := tx.CreateExchangeAccount(testAccount()); err != nil {
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

	base.MarketType = "SPOT"
	base.Side = "SELL"
	err = s.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.InsertFill(base)
		return err
	})
	require.ErrorIs(t, err, ErrInvalidRecord)

	base.Side = "BUY"
	base.PositionSide = "NET"
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
		if err := tx.CreateExchangeAccount(testAccount()); err != nil {
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

func TestCreateOrderValidatesEveryDecimal(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OrderRecord)
	}{
		{"quantity zero", func(v *OrderRecord) { v.Quantity = "0" }},
		{"quantity negative", func(v *OrderRecord) { v.Quantity = "-1" }},
		{"quantity malformed", func(v *OrderRecord) { v.Quantity = "many" }},
		{"reference price zero", func(v *OrderRecord) { v.ReferencePrice = "0" }},
		{"limit price negative", func(v *OrderRecord) { *v.LimitPrice = "-1" }},
		{"filled negative", func(v *OrderRecord) { v.FilledQuantity = "-1" }},
		{"filled whitespace", func(v *OrderRecord) { v.FilledQuantity = " " }},
		{"average negative", func(v *OrderRecord) { v.AveragePrice = "-1" }},
		{"reserved negative", func(v *OrderRecord) { v.ReservedQuantity = "-1" }},
		{"remaining negative", func(v *OrderRecord) { v.RemainingReservedQuantity = "-1" }},
		{"filled exceeds quantity", func(v *OrderRecord) { v.FilledQuantity = "2" }},
		{"remaining exceeds reserved", func(v *OrderRecord) {
			v.ReservedQuantity, v.RemainingReservedQuantity = "1", "2"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			require.NoError(t, seedAccountAndInstrument(ctx, s))
			limit := "100"
			record := OrderRecord{
				SpaceID: "space-1", OrderID: "decimal-order",
				ExchangeAccountID: "account-1", ClientOrderID: "decimal-client",
				Symbol: "BTCUSDT", OrderType: "LIMIT", TimeInForce: "GTC",
				Side: "BUY", Quantity: "1", LimitPrice: &limit,
				ReferencePrice: "100", Source: "test", State: "READY",
			}
			tt.mutate(&record)
			err := s.Transaction(ctx, func(tx *Tx) error {
				return tx.CreateOrder(record)
			})
			require.ErrorIs(t, err, ErrInvalidRecord)
		})
	}
}

func TestInsertFillValidatesDecimalsAndCanonicalizesReplay(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*FillRecord)
	}{
		{"price zero", func(v *FillRecord) { v.Price = "0" }},
		{"quantity negative", func(v *FillRecord) { v.Quantity = "-1" }},
		{"fee negative", func(v *FillRecord) { v.Fee = "-1" }},
		{"realized malformed", func(v *FillRecord) { v.RealizedPnL = "many" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			require.NoError(t, seedOrder(ctx, s))
			record := FillRecord{
				SpaceID: "space-1", FillID: "decimal-fill",
				ExchangeTradeID: "decimal-trade", OrderID: "order-1",
				Price: "100", Quantity: "1", Fee: "0", RealizedPnL: "-1",
			}
			tt.mutate(&record)
			err := s.Transaction(ctx, func(tx *Tx) error {
				_, err := tx.InsertFill(record)
				return err
			})
			require.ErrorIs(t, err, ErrInvalidRecord)
		})
	}

	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, seedOrder(ctx, s))
	record := FillRecord{
		SpaceID: "space-1", FillID: "canonical-fill",
		ExchangeTradeID: "canonical-trade", OrderID: "order-1",
		Price: "100.00", Quantity: "1.0", Fee: "0.10", RealizedPnL: "-1.00",
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		inserted, err := tx.InsertFill(record)
		require.True(t, inserted)
		return err
	}))
	record.Price, record.Quantity, record.Fee, record.RealizedPnL = "100", "1", "0.1", "-1"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		inserted, err := tx.InsertFill(record)
		require.False(t, inserted)
		return err
	}))
}

func TestUpsertPositionValidatesSignedAndNonNegativeDecimals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PositionRecord)
	}{
		{"quantity malformed", func(v *PositionRecord) { v.SignedQuantity = "many" }},
		{"entry negative", func(v *PositionRecord) { v.EntryPrice = "-1" }},
		{"mark negative", func(v *PositionRecord) { v.MarkPrice = "-1" }},
		{"leverage negative", func(v *PositionRecord) { v.Leverage = "-1" }},
		{"margin negative", func(v *PositionRecord) { v.UsedMargin = "-1" }},
		{"liquidation negative", func(v *PositionRecord) { v.LiquidationPrice = "-1" }},
		{"pnl malformed", func(v *PositionRecord) { v.RealizedPnL = "many" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
				return tx.CreateExchangeAccount(testAccount())
			}))
			record := PositionRecord{
				SpaceID: "space-1", ExchangeAccountID: "account-1",
				Symbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "-1",
				EntryPrice: "100", MarkPrice: "101", Leverage: "0",
				UsedMargin: "0", LiquidationPrice: "0",
				UnrealizedPnL: "-1", RealizedPnL: "2",
			}
			tt.mutate(&record)
			err := s.Transaction(ctx, func(tx *Tx) error {
				return tx.UpsertPosition(record)
			})
			require.ErrorIs(t, err, ErrInvalidRecord)
		})
	}
}

func TestUpsertSwapPositionRequiresPositiveLeverageAndAllowsSignedPnL(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	account := testAccount()
	account.MarketType = "SWAP"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(account)
	}))
	position := PositionRecord{
		SpaceID: "space-1", ExchangeAccountID: "account-1",
		Symbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "-1.00",
		EntryPrice: "100", MarkPrice: "101", UsedMargin: "10",
		UnrealizedPnL: "-2.00", RealizedPnL: "-1.00",
	}
	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertPosition(position)
	})
	require.ErrorIs(t, err, ErrInvalidRecord)

	position.Leverage = "5.0"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertPosition(position)
	}))
	var row struct {
		Quantity      string `gorm:"column:c_signed_quantity"`
		Leverage      string `gorm:"column:c_leverage"`
		UnrealizedPnL string `gorm:"column:c_unrealized_pnl"`
	}
	require.NoError(t, s.db.Table("t_exchange_positions").Take(&row).Error)
	require.Equal(t, "-1", row.Quantity)
	require.Equal(t, "5", row.Leverage)
	require.Equal(t, "-2", row.UnrealizedPnL)
}

func seedOrder(ctx context.Context, s *Store) error {
	if err := seedAccountAndInstrument(ctx, s); err != nil {
		return err
	}
	return s.Transaction(ctx, func(tx *Tx) error {
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

func seedAccountAndInstrument(ctx context.Context, s *Store) error {
	return s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateExchangeAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01",
			ExchangeQuantityStep: "0.0001", Status: "TRADING",
		}); err != nil {
			return err
		}
		return nil
	})
}

func stringPointer(value string) *string { return &value }
