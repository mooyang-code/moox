package consumer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSpace   = "space-1"
	testAccount = "account-1"
	testSymbol  = "BTC-USDT"
)

func openFillStore(t *testing.T, market exchange.MarketType) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: testSpace, TradingAccountID: testAccount, Name: "primary",
			Exchange: string(exchange.ExchangeBinance), MarketType: string(market),
			ExecutionMode:      string(exchange.ExecutionModePaper),
			Environment:        string(exchange.AccountEnvironmentPaper),
			CredentialSecretID: "secret-1", SettlementAsset: "USDT",
			MarginMode: string(exchange.MarginModeCross), Status: string(exchange.AccountStatusEnabled),
			LeverageSettings: store.LeverageSettings{testSymbol: "10"},
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: string(exchange.ExchangeBinance), MarketType: string(market),
			Symbol: testSymbol, InstrumentID: testSymbol, BaseAsset: "BTC",
			QuoteAsset: "USDT", SettlementAsset: "USDT", Linear: market == exchange.MarketTypeSwap,
			ContractValue: "1", ContractValueAsset: "BTC", ExchangeQuantityStep: "0.001",
			MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	return s
}

func seedFillOrder(
	t *testing.T,
	s *store.Store,
	market exchange.MarketType,
	state order.State,
	quantity string,
	reserved string,
) store.OrderRecord {
	t.Helper()
	positionSide := ""
	if market == exchange.MarketTypeSwap {
		positionSide = string(exchange.PositionSideNet)
	}
	record := store.OrderRecord{
		SpaceID: testSpace, OrderID: "order-1", TradingAccountID: testAccount,
		ClientOrderID: "client-1", ExchangeOrderID: "exchange-order-1",
		Exchange: string(exchange.ExchangeBinance), MarketType: string(market),
		Symbol: testSymbol, OrderType: string(exchange.OrderTypeMarket),
		Side: string(exchange.SideBuy), PositionSide: positionSide,
		Quantity: quantity, ReferencePrice: "100", ReferencePriceAt: time.Now().UnixMilli(),
		OwnerType: "EXTERNAL", OwnerID: "exchange-order-1",
		State: string(state), ReservedAsset: "USDT",
		ReservedQuantity: reserved, RemainingReservedQuantity: reserved, Version: 1,
	}
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(record)
	}))
	return record
}

func fillSource() Source {
	return Source{
		SpaceID: testSpace, TradingAccountID: testAccount, Kind: OriginPrivateSocket,
	}
}

func spotFill(id string, quantity string) exchange.Fill {
	return exchange.Fill{
		ExchangeTradeID: id, ExchangeOrderID: "exchange-order-1",
		ClientOrderID: "client-1", Symbol: testSymbol, Side: exchange.SideBuy,
		Quantity: shared.MustDecimal(quantity), Price: shared.MustDecimal("100"),
		Fee: shared.MustDecimal("1"), FeeAsset: "USDT", TradedAt: time.Now(),
	}
}

func TestReducerApplyFillSpotPersistsFillAndReservationOnce(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSpot)
	seedFillOrder(t, s, exchange.MarketTypeSpot, order.Open, "1", "100")
	reducer := Reducer{Store: s}
	fill := spotFill("trade-1", "0.5")

	applied, err := reducer.ApplyFill(context.Background(), fill, fillSource())
	require.NoError(t, err)
	assert.True(t, applied)

	var got struct {
		State                     string `gorm:"column:c_state"`
		FilledQuantity            string `gorm:"column:c_filled_quantity"`
		AveragePrice              string `gorm:"column:c_average_price"`
		RemainingReservedQuantity string `gorm:"column:c_remaining_reserved_quantity"`
		Version                   uint64 `gorm:"column:c_version"`
	}
	require.NoError(t, s.DBForTest().Raw(`
		SELECT c_state, c_filled_quantity, c_average_price,
			c_remaining_reserved_quantity, c_version
		FROM t_trade_orders WHERE c_space_id = ? AND c_order_id = ?
	`, testSpace, "order-1").Scan(&got).Error)
	assert.Equal(t, string(order.PartiallyFilled), got.State)
	assert.Equal(t, "0.5", got.FilledQuantity)
	assert.Equal(t, "100", got.AveragePrice)
	assert.Equal(t, "50", got.RemainingReservedQuantity)
	assert.Equal(t, uint64(2), got.Version)

	applied, err = reducer.ApplyFill(context.Background(), fill, Source{
		SpaceID: testSpace, TradingAccountID: testAccount, Kind: OriginRESTSnapshot,
	})
	require.NoError(t, err)
	assert.False(t, applied)

	var fillCount int64
	require.NoError(t, s.DBForTest().Table("t_order_fills").Count(&fillCount).Error)
	assert.Equal(t, int64(1), fillCount)
}

func TestReducerApplyFillRepairsLateCanceledOrder(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSpot)
	seedFillOrder(t, s, exchange.MarketTypeSpot, order.Canceled, "1", "100")
	reducer := Reducer{Store: s}

	applied, err := reducer.ApplyFill(
		context.Background(), spotFill("late-trade", "0.25"), fillSource(),
	)
	require.NoError(t, err)
	assert.True(t, applied)

	var got struct {
		State          string `gorm:"column:c_state"`
		FilledQuantity string `gorm:"column:c_filled_quantity"`
	}
	require.NoError(t, s.DBForTest().Raw(`
		SELECT c_state, c_filled_quantity FROM t_trade_orders
		WHERE c_space_id = ? AND c_order_id = ?
	`, testSpace, "order-1").Scan(&got).Error)
	assert.Equal(t, string(order.PartiallyCanceled), got.State)
	assert.Equal(t, "0.25", got.FilledQuantity)
}

func TestReducerConfirmCancelAppliesFinalFillBeforeRelease(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSpot)
	seedFillOrder(t, s, exchange.MarketTypeSpot, order.Canceling, "1", "100")
	reducer := Reducer{Store: s}

	applied, err := reducer.ApplyFill(
		context.Background(), spotFill("final-before-cancel", "0.25"), fillSource(),
	)
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, reducer.ConfirmCancel(context.Background(), testSpace, "order-1"))

	got, err := s.GetOrder(context.Background(), testSpace, "order-1")
	require.NoError(t, err)
	require.Equal(t, string(order.PartiallyCanceled), got.State)
	require.Equal(t, "0.25", got.FilledQuantity)
	require.Equal(t, "0", got.RemainingReservedQuantity)
	require.Positive(t, got.FinishedAt)
}

func TestReducerApplyFillReleasesUnusedReservationWhenFilled(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSpot)
	seedFillOrder(t, s, exchange.MarketTypeSpot, order.Open, "1", "100")
	reducer := Reducer{Store: s}
	fill := spotFill("price-improvement", "1")
	fill.Price = shared.MustDecimal("90")

	applied, err := reducer.ApplyFill(context.Background(), fill, fillSource())
	require.NoError(t, err)
	assert.True(t, applied)

	got, err := s.GetOrder(context.Background(), testSpace, "order-1")
	require.NoError(t, err)
	assert.Equal(t, string(order.Filled), got.State)
	assert.Equal(t, "0", got.RemainingReservedQuantity)
	assert.Positive(t, got.FinishedAt)
}

func TestReducerApplyFillRoundsAveragePriceToInstrumentTickScale(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSpot)
	seedFillOrder(t, s, exchange.MarketTypeSpot, order.Open, "3", "303")
	reducer := Reducer{Store: s}
	first := spotFill("average-1", "1")
	second := spotFill("average-2", "2")
	second.Price = shared.MustDecimal("101")

	applied, err := reducer.ApplyFill(context.Background(), first, fillSource())
	require.NoError(t, err)
	assert.True(t, applied)
	applied, err = reducer.ApplyFill(context.Background(), second, fillSource())
	require.NoError(t, err)
	assert.True(t, applied)

	got, err := s.GetOrder(context.Background(), testSpace, "order-1")
	require.NoError(t, err)
	assert.Equal(t, "100.7", got.AveragePrice)
}

func TestReducerApplyFillSwapRecordsPnLAndEstimatesPosition(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSwap)
	seedFillOrder(t, s, exchange.MarketTypeSwap, order.Open, "2", "20")
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: testSpace, TradingAccountID: testAccount, Symbol: testSymbol,
			PositionSide: string(exchange.PositionSideNet), SignedQuantity: "-1",
			EntryPrice: "90", MarkPrice: "90", Leverage: "10",
			MarginMode: string(exchange.MarginModeCross),
		})
	}))
	fill := exchange.Fill{
		ExchangeTradeID: "swap-trade", ExchangeOrderID: "exchange-order-1",
		ClientOrderID: "client-1", Symbol: testSymbol, Side: exchange.SideBuy,
		PositionSide: exchange.PositionSideNet, Quantity: shared.MustDecimal("2"),
		Price: shared.MustDecimal("100"), Fee: shared.MustDecimal("0.1"),
		FeeAsset: "USDT", RealizedPnL: shared.MustDecimal("3"),
		SettlementAsset: "USDT", TradedAt: time.Now(),
	}
	reducer := Reducer{Store: s}

	applied, err := reducer.ApplyFill(context.Background(), fill, fillSource())
	require.NoError(t, err)
	assert.True(t, applied)

	var position struct {
		SignedQuantity string `gorm:"column:c_signed_quantity"`
		EntryPrice     string `gorm:"column:c_entry_price"`
		RealizedPnL    string `gorm:"column:c_realized_pnl"`
	}
	require.NoError(t, s.DBForTest().Raw(`
		SELECT c_signed_quantity, c_entry_price, c_realized_pnl
		FROM t_trading_positions
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_symbol = ?
	`, testSpace, testAccount, testSymbol).Scan(&position).Error)
	assert.Equal(t, "1", position.SignedQuantity)
	assert.Equal(t, "100", position.EntryPrice)
	assert.Equal(t, "3", position.RealizedPnL)

}

func TestReducerApplyFillCreatesFirstSwapPosition(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSwap)
	seedFillOrder(t, s, exchange.MarketTypeSwap, order.Open, "1", "10")
	fill := exchange.Fill{
		ExchangeTradeID: "first-swap-trade", ExchangeOrderID: "exchange-order-1",
		ClientOrderID: "client-1", Symbol: testSymbol, Side: exchange.SideBuy,
		PositionSide: exchange.PositionSideNet, Quantity: shared.MustDecimal("1"),
		Price: shared.MustDecimal("100"), SettlementAsset: "USDT", TradedAt: time.Now(),
	}

	applied, err := (&Reducer{Store: s}).ApplyFill(
		context.Background(),
		fill,
		fillSource(),
	)
	require.NoError(t, err)
	require.True(t, applied)

	var position struct {
		SignedQuantity string `gorm:"column:c_signed_quantity"`
		EntryPrice     string `gorm:"column:c_entry_price"`
		Leverage       string `gorm:"column:c_leverage"`
	}
	require.NoError(t, s.DBForTest().Raw(`
		SELECT c_signed_quantity, c_entry_price, c_leverage
		FROM t_trading_positions
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_symbol = ?
	`, testSpace, testAccount, testSymbol).Scan(&position).Error)
	require.Equal(t, "1", position.SignedQuantity)
	require.Equal(t, "100", position.EntryPrice)
	require.Equal(t, "10", position.Leverage)
}

func TestReducerApplyFillRollsBackAllWritesOnError(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSpot)
	seedFillOrder(t, s, exchange.MarketTypeSpot, order.Open, "1", "100")
	reducer := Reducer{Store: s}

	_, err := reducer.ApplyFill(
		context.Background(), spotFill("overfill", "2"), fillSource(),
	)
	require.Error(t, err)

	var fillCount int64
	require.NoError(t, s.DBForTest().Table("t_order_fills").Count(&fillCount).Error)
	assert.Zero(t, fillCount)
	var got struct {
		State          string `gorm:"column:c_state"`
		FilledQuantity string `gorm:"column:c_filled_quantity"`
	}
	require.NoError(t, s.DBForTest().Raw(`
		SELECT c_state, c_filled_quantity FROM t_trade_orders
		WHERE c_space_id = ? AND c_order_id = ?
	`, testSpace, "order-1").Scan(&got).Error)
	assert.Equal(t, string(order.Open), got.State)
	assert.Equal(t, "0", got.FilledQuantity)
}

func TestReducerApplyFillRejectsNonPositiveExchangeTimestampWithoutWrites(t *testing.T) {
	s := openFillStore(t, exchange.MarketTypeSpot)
	seedFillOrder(t, s, exchange.MarketTypeSpot, order.Open, "1", "100")
	fill := spotFill("missing-time", "1")
	fill.TradedAt = time.UnixMilli(0)

	applied, err := (&Reducer{Store: s}).ApplyFill(
		context.Background(), fill, fillSource(),
	)
	require.Error(t, err)
	require.False(t, applied)

	var fillCount int64
	require.NoError(t, s.DBForTest().Table("t_order_fills").Count(&fillCount).Error)
	require.Zero(t, fillCount)
	got, getErr := s.GetOrder(context.Background(), testSpace, "order-1")
	require.NoError(t, getErr)
	require.Equal(t, string(order.Open), got.State)
	require.Equal(t, "0", got.FilledQuantity)
	require.Zero(t, got.FinishedAt)
}
