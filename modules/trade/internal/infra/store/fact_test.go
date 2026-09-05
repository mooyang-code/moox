package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreateOrderPersistsTrustedOwnership(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, seedAccountAndInstrument(ctx, s))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", ExecutionMode: "PAPER",
			MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "account-1", Enabled: true,
		}); err != nil {
			return err
		}
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "order-target",
			TradingAccountID: "account-1", ClientOrderID: "client-target",
			ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
			Quantity: "1", ReferencePrice: "100",
			OwnerType: "TARGET", OwnerID: "target-1",
			LogicalAccountID: "logical-1", RunnerID: "runner-1",
			State: "PENDING",
		})
	}))

	got, err := s.GetOrder(ctx, "space-1", "order-target")
	require.NoError(t, err)
	require.Equal(t, "TARGET", got.OwnerType)
	require.Equal(t, "target-1", got.OwnerID)
	require.Equal(t, "logical-1", got.LogicalAccountID)
	require.Equal(t, "runner-1", got.RunnerID)

	err = s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "invalid-target",
			TradingAccountID: "account-1", ClientOrderID: "invalid-target",
			ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
			Quantity: "1", ReferencePrice: "100",
			OwnerType: "TARGET", OwnerID: "target-1",
			LogicalAccountID: "logical-1", State: "PENDING",
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

func TestInsertFillDeduplicatesExchangeTradeIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, seedOrder(ctx, s))

	fill := FillRecord{
		SpaceID: "space-1", FillID: "fill-1", ExchangeTradeID: "trade-1",
		OrderID: "order-1", ExchangeOrderID: "exchange-order-1",
		TradingAccountID: "account-1", Exchange: "BINANCE",
		MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", Side: "BUY",
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
			SpaceID: "space-1", OrderID: "order-2", TradingAccountID: "account-1",
			ClientOrderID: "client-order-2", ExchangeOrderID: "exchange-order-2",
			ExchangeSymbol: "BTCUSDT", OrderType: "LIMIT", TimeInForce: "GTC",
			Side: "BUY", Quantity: "1", LimitPrice: stringPointer("100"),
			ReferencePrice: "100", OwnerType: "EXTERNAL",
			OwnerID: "exchange-order-2", State: "OPEN",
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
			SpaceID: "space-1", OrderID: "order-2", TradingAccountID: "account-1",
			ClientOrderID: "client-order-1", Exchange: "BINANCE",
			MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET",
			Side: "BUY", Quantity: "1", ReferencePrice: "100",
			OwnerType: "EXTERNAL", OwnerID: "external-2", State: "READY",
		})
	})
	require.ErrorIs(t, err, ErrConflict)
}

func TestRPCQueriesFilterAndPaginateOrdersFillsAndPositions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, seedOrder(ctx, s))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "order-2", TradingAccountID: "account-1",
			ClientOrderID: "client-order-2", ExchangeOrderID: "exchange-order-2",
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			OrderType: "MARKET", Side: "SELL", Quantity: "1",
			ReferencePrice: "101", OwnerType: "EXTERNAL",
			OwnerID: "exchange-order-2", State: "FILLED",
			FilledQuantity: "1", AveragePrice: "101", FinishedAt: 2_000,
		}); err != nil {
			return err
		}
		inserted, err := tx.InsertFill(FillRecord{
			SpaceID: "space-1", FillID: "fill-1", ExchangeTradeID: "trade-1",
			OrderID: "order-2", ExchangeOrderID: "exchange-order-2",
			TradingAccountID: "account-1", Exchange: "BINANCE",
			MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", Side: "SELL",
			Price: "101", Quantity: "1", Fee: "0", TradedAt: 1_500,
		})
		if err != nil || !inserted {
			return err
		}
		return tx.UpsertPosition(PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			ExchangeUpdatedAt: 1_500,
		})
	}))
	now := time.Now()
	orders, total, err := s.ListOrders(ctx, "space-1", OrderQuery{
		TradingAccountID: "account-1", StartTime: now.Add(-time.Minute).UnixMilli(),
		EndTime: now.Add(time.Minute).UnixMilli(), Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, int64(2), total)
	open, openTotal, err := s.ListOrders(ctx, "space-1", OrderQuery{
		TradingAccountID: "account-1", OnlyOpen: true, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, open, 1)
	require.Equal(t, int64(1), openTotal)
	fills, fillTotal, err := s.ListFills(ctx, "space-1", FillQuery{
		TradingAccountID: "account-1", StartTime: 1_000, EndTime: 2_000,
		Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, fills, 1)
	require.Equal(t, int64(1), fillTotal)
	positions, err := s.ListPositions(ctx, "space-1", "account-1", "BTCUSDT")
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.False(t, positions[0].UpdatedAt.IsZero())
}

func TestCanonicalInstrumentQueriesDoNotUseExchangeSymbol(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", InstrumentID: "BTC-USDT-SPOT",
			ExchangeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			PriceTick: "0.01", ExchangeQuantityStep: "0.0001", Status: "TRADING",
		}); err != nil {
			return err
		}
		if err := tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "canonical-order", TradingAccountID: "account-1",
			ClientOrderID: "canonical-client", Exchange: "BINANCE", MarketType: "SPOT",
			InstrumentID: "BTC-USDT-SPOT", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET",
			Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL",
			OwnerID: "canonical-order", State: "OPEN",
		}); err != nil {
			return err
		}
		inserted, err := tx.InsertFill(FillRecord{
			SpaceID: "space-1", FillID: "canonical-fill", ExchangeTradeID: "canonical-trade",
			OrderID: "canonical-order", TradingAccountID: "account-1", Exchange: "BINANCE",
			MarketType: "SPOT", InstrumentID: "BTC-USDT-SPOT", ExchangeSymbol: "BTCUSDT",
			Side: "BUY", Price: "100", Quantity: "1", TradedAt: 100,
		})
		if err != nil || !inserted {
			return err
		}
		return tx.UpsertPosition(PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", InstrumentID: "BTC-USDT-SPOT",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
		})
	}))

	orders, total, err := s.ListOrders(ctx, "space-1", OrderQuery{
		TradingAccountID: "account-1", InstrumentID: "BTC-USDT-SPOT", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, orders, 1)
	fills, total, err := s.ListFills(ctx, "space-1", FillQuery{
		TradingAccountID: "account-1", InstrumentID: "BTC-USDT-SPOT", Limit: 10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, fills, 1)
	positions, err := s.ListPositionsByInstrument(ctx, "space-1", "account-1", "BTC-USDT-SPOT")
	require.NoError(t, err)
	require.Len(t, positions, 1)
}

func TestExchangeInstrumentSwapFieldsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	want := InstrumentRecord{
		Exchange: "OKX", Environment: "PRODUCTION", MarketType: "SWAP", ExchangeSymbol: "BTC-USDT-SWAP",
		InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
		SettlementAsset: "USDT", Linear: true, ContractValue: "0.01",
		ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
		MinExchangeQuantity: "1", PriceTick: "0.1", MinNotional: "10",
		Status: "TRADING", ExchangeUpdatedAt: 123,
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertInstrument(want)
	}))

	got, err := s.GetInstrument(ctx, want.Exchange, want.MarketType, want.ExchangeSymbol)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestExchangeInstrumentRejectsIncompleteSwapConversion(t *testing.T) {
	s := openTestStore(t)
	err := s.Transaction(context.Background(), func(tx *Tx) error {
		return tx.UpsertInstrument(InstrumentRecord{
			Exchange: "OKX", MarketType: "SWAP", ExchangeSymbol: "BTC-USDT-SWAP",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ExchangeQuantityStep: "1", PriceTick: "0.1", Status: "TRADING",
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)

	err = s.db.Exec(`
		INSERT INTO t_trade_instruments (
			c_exchange, c_environment, c_market_type, c_exchange_symbol, c_base_asset, c_quote_asset,
			c_settlement_asset, c_linear, c_contract_value, c_contract_value_asset,
			c_exchange_quantity_step, c_min_exchange_quantity, c_price_tick, c_status
		) VALUES ('OKX', 'PRODUCTION', 'SWAP', 'ETH-USDT-SWAP', 'ETH', 'USDT', 'USDT', 0,
			'0', '', '1', '0', '0.01', 'TRADING')
	`).Error
	require.Error(t, err)
}

func TestCreateOrderRejectsContradictoryExchangeIdentity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		return tx.UpsertInstrument(InstrumentRecord{
			Exchange: "OKX", MarketType: "SWAP", ExchangeSymbol: "BTC-USDT-SWAP",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			Linear: true, ContractValue: "0.01", ContractValueAsset: "BTC",
			ExchangeQuantityStep: "1", MinExchangeQuantity: "1",
			PriceTick: "0.1", Status: "TRADING",
		})
	}))

	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "order-wrong", TradingAccountID: "account-1",
			ClientOrderID: "client-wrong", Exchange: "OKX", MarketType: "SWAP",
			ExchangeSymbol: "BTC-USDT-SWAP", OrderType: "MARKET", Side: "BUY",
			Quantity: "1", ReferencePrice: "100",
			OwnerType: "EXTERNAL", OwnerID: "external-wrong", State: "READY",
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

func TestCreateOrderDerivesExchangeIdentityFromAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", ExchangeQuantityStep: "0.0001",
			PriceTick: "0.01", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "derived-order",
			TradingAccountID: "account-1", ClientOrderID: "derived-client",
			ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
			Quantity: "1", ReferencePrice: "100",
			OwnerType: "EXTERNAL", OwnerID: "external-derived", State: "READY",
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
		OrderID: "order-1", TradingAccountID: "other-account",
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
		Side: "BUY", Price: "100", Quantity: "1",
	}
	err := s.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.InsertFill(base)
		return err
	})
	require.ErrorIs(t, err, ErrInvalidRecord)

	base.TradingAccountID = "account-1"
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
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		return tx.UpsertPosition(PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
		})
	}))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertPosition(PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "-2",
		})
	}))

	var rows []struct {
		Quantity string `gorm:"column:c_signed_quantity"`
	}
	require.NoError(t, s.db.Table("t_trading_positions").Find(&rows).Error)
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
				TradingAccountID: "account-1", ClientOrderID: "decimal-client",
				ExchangeSymbol: "BTCUSDT", OrderType: "LIMIT", TimeInForce: "GTC",
				Side: "BUY", Quantity: "1", LimitPrice: &limit,
				ReferencePrice: "100", OwnerType: "EXTERNAL",
				OwnerID: "external-decimal", State: "READY",
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
		{"fee malformed", func(v *FillRecord) { v.Fee = "rebate" }},
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
				return tx.CreateTradingAccount(testAccount())
			}))
			record := PositionRecord{
				SpaceID: "space-1", TradingAccountID: "account-1",
				ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "-1",
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
		return tx.CreateTradingAccount(account)
	}))
	position := PositionRecord{
		SpaceID: "space-1", TradingAccountID: "account-1",
		ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "-1.00",
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
	require.NoError(t, s.db.Table("t_trading_positions").Take(&row).Error)
	require.Equal(t, "-1", row.Quantity)
	require.Equal(t, "5", row.Leverage)
	require.Equal(t, "-2", row.UnrealizedPnL)
}

func TestReplacePositionsPreservesNewerPrivateUpdate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	account := testAccount()
	account.MarketType = "SWAP"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		for _, position := range []PositionRecord{
			{
				SpaceID: "space-1", TradingAccountID: "account-1",
				ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "2",
				Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 1_800,
			},
			{
				SpaceID: "space-1", TradingAccountID: "account-1",
				ExchangeSymbol: "ETHUSDT", PositionSide: "NET", SignedQuantity: "1",
				Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 1_000,
			},
		} {
			if err := tx.UpsertPosition(position); err != nil {
				return err
			}
		}
		return tx.ReplacePositionsForAccount(
			"space-1",
			"account-1",
			[]PositionRecord{{
				SpaceID: "space-1", TradingAccountID: "account-1",
				ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
				Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 1_500,
			}},
			2_000,
		)
	}))
	var btc PositionRecord
	var found bool
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		var err error
		btc, found, err = tx.GetPosition("space-1", "account-1", "BTCUSDT", "NET")
		return err
	}))
	require.True(t, found)
	require.Equal(t, "2", btc.SignedQuantity)
	require.Equal(t, int64(1_800), btc.ExchangeUpdatedAt)
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		_, exists, err := tx.GetPosition("space-1", "account-1", "ETHUSDT", "NET")
		require.False(t, exists)
		return err
	}))
}

func seedOrder(ctx context.Context, s *Store) error {
	if err := seedAccountAndInstrument(ctx, s); err != nil {
		return err
	}
	return s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateOrder(OrderRecord{
			SpaceID: "space-1", OrderID: "order-1", TradingAccountID: "account-1",
			ClientOrderID: "client-order-1", ExchangeOrderID: "exchange-order-1",
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY",
			Quantity: "1", LimitPrice: stringPointer("100"), ReferencePrice: "100",
			OwnerType: "EXTERNAL", OwnerID: "exchange-order-1",
			State: "OPEN", Version: 1,
		})
	})
}

func seedAccountAndInstrument(ctx context.Context, s *Store) error {
	return s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01",
			ExchangeQuantityStep: "0.0001", Status: "TRADING",
		}); err != nil {
			return err
		}
		return nil
	})
}

func stringPointer(value string) *string { return &value }
