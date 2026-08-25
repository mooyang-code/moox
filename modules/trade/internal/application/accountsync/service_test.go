package accountsync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type syncAdapterSource struct {
	adapter exchange.Adapter
}

type readySessionState bool

func (s readySessionState) Ready(string) bool { return bool(s) }

func (s syncAdapterSource) Adapter(string) (exchange.Adapter, error) {
	return s.adapter, nil
}

type paperPublicAdapter struct{ *syncAdapter }

func (*paperPublicAdapter) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return []exchange.Instrument{{
		Exchange: exchange.ExchangeBinance, MarketType: exchange.MarketTypeSpot,
		Symbol: "BTCUSDT", InstrumentID: "BTC-USDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
		ExchangeQuantityStep: shared.MustDecimal("0.0001"),
		PriceTick:            shared.MustDecimal("0.01"), Status: "TRADING",
	}}, nil
}

func (*paperPublicAdapter) GetReferencePrice(
	context.Context,
	string,
) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{
		Price: shared.MustDecimal("50000"), UpdatedAt: time.Now().UTC(),
	}, nil
}

type syncAdapter struct {
	order       exchange.Order
	fill        exchange.Fill
	fillStarted chan struct{}
	fillRelease chan struct{}
	lookupErr   error
}

func (*syncAdapter) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (*syncAdapter) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return nil, nil
}
func (*syncAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{
		AvailableFunds:    shared.MustDecimal("900"),
		Equity:            shared.MustDecimal("1000"),
		ExchangeUpdatedAt: time.UnixMilli(2_000),
	}, nil
}
func (*syncAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return []exchange.Position{{
		Symbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("0.5"),
		EntryPrice:     shared.MustDecimal("100"), MarkPrice: shared.MustDecimal("101"),
		Leverage: shared.MustDecimal("5"), MarginMode: exchange.MarginModeCross,
		ExchangeUpdatedAt: time.UnixMilli(2_000),
	}}, nil
}
func (a *syncAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return []exchange.Order{a.order}, nil
}
func (a *syncAdapter) ListRecentFills(
	context.Context,
	string,
	string,
) ([]exchange.Fill, string, error) {
	if a.fillStarted != nil {
		close(a.fillStarted)
		<-a.fillRelease
	}
	return []exchange.Fill{a.fill}, "11", nil
}
func (a *syncAdapter) GetOrder(context.Context, string, string) (exchange.Order, error) {
	return a.order, nil
}
func (a *syncAdapter) GetOrderByExchangeID(
	_ context.Context,
	symbol string,
	exchangeOrderID string,
) (exchange.Order, error) {
	if a.lookupErr != nil {
		return exchange.Order{}, a.lookupErr
	}
	current := a.order
	current.Symbol = symbol
	current.ExchangeOrderID = exchangeOrderID
	return current, nil
}
func (*syncAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*syncAdapter) CancelOrder(context.Context, string, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*syncAdapter) SetLeverage(context.Context, string, shared.Decimal) error {
	return nil
}
func (*syncAdapter) SetMarginMode(context.Context, string, exchange.MarginMode) error {
	return nil
}
func (*syncAdapter) Subscribe(context.Context, exchange.EventHandler) error {
	return nil
}

func TestServiceSyncAccountImportsExternalOrderAndAppliesFacts(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{
		order: exchange.Order{
			ExchangeOrderID: "exchange-1", ClientOrderID: "manual-1",
			Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.5"),
			AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusPartiallyFilled,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
		},
		fill: exchange.Fill{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "exchange-1",
			ClientOrderID: "manual-1", Symbol: "BTC-USDT", Side: exchange.SideBuy,
			PositionSide: exchange.PositionSideNet,
			Quantity:     shared.MustDecimal("0.5"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_500),
		},
	}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(3_000) },
	}

	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, 1, result.FillsIngested)
	require.Equal(t, 1, result.OrdersUpdated)
	require.Equal(t, 1, result.PositionsUpdated)
	require.True(t, result.AccountSnapshotUpdated)
	require.True(t, result.Ready)

	orderRecord, err := tradeStore.GetOrderByClientID(
		context.Background(),
		"space-1",
		"account-1",
		"manual-1",
	)
	require.NoError(t, err)
	require.Equal(t, "EXTERNAL", orderRecord.OwnerType)
	require.Equal(t, "PARTIALLY_FILLED", orderRecord.State)
	require.Equal(t, "0.5", orderRecord.FilledQuantity)

	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.True(t, account.Ready)
	require.Equal(t, "11", account.FillCursors["BTC-USDT"])
	require.Equal(t, int64(3_000), account.LastSyncAt)
	require.Equal(t, int64(2_000), account.SnapshotSourceTime)

	result, err = service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.Zero(t, result.FillsIngested, "replayed REST Fill must be idempotent")
}

func TestExternalOrderPausesOwningLogicalAccount(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	observer := runFactsObserver(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Facts: observer,
		Now:   func() time.Time { return time.UnixMilli(3_000) },
	}
	current := exchange.Order{
		ExchangeOrderID: "external-order", ClientOrderID: "outside-client",
		Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(2_000), UpdatedAt: time.UnixMilli(2_000),
	}

	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))

	require.Eventually(t, func() bool {
		logicalAccount, err := tradeStore.GetLogicalAccount(
			context.Background(), "space-1", "logical-1",
		)
		return err == nil &&
			logicalAccount.AutomationState == "PAUSED" &&
			strings.Contains(logicalAccount.PauseReason, "EXTERNAL")
	}, time.Second, 10*time.Millisecond)
}

func TestExternalFillImportsExternalOwnerAndPauses(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	adapter := &syncAdapter{lookupErr: &exchange.Error{
		Kind: exchange.ErrorOrderNotFound, Err: errors.New("missing"),
	}}
	observer := runFactsObserver(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore},
		Facts: observer,
		Now:   func() time.Time { return time.UnixMilli(3_000) },
	}

	applied, err := service.ApplyFill(context.Background(), "account-1", exchange.Fill{
		ExchangeTradeID: "external-trade", ExchangeOrderID: "external-order",
		Symbol: "BTC-USDT", Side: exchange.SideBuy,
		PositionSide: exchange.PositionSideNet,
		Quantity:     shared.MustDecimal("0.5"), Price: shared.MustDecimal("100"),
		SettlementAsset: "USDT", TradedAt: time.UnixMilli(2_000),
	})
	require.NoError(t, err)
	require.True(t, applied)

	order, err := tradeStore.GetOrderByExchangeID(
		context.Background(), "space-1", "account-1", "BTC-USDT", "external-order",
	)
	require.NoError(t, err)
	require.Equal(t, "EXTERNAL", order.OwnerType)
	require.Eventually(t, func() bool {
		logicalAccount, err := tradeStore.GetLogicalAccount(
			context.Background(), "space-1", "logical-1",
		)
		return err == nil &&
			logicalAccount.AutomationState == "PAUSED" &&
			strings.Contains(logicalAccount.PauseReason, "EXTERNAL")
	}, time.Second, 10*time.Millisecond)
}

func TestKnownExternalFillPausesOnceForNewFact(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	observer := runFactsObserver(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Facts: observer,
		Now:   func() time.Time { return time.UnixMilli(4_000) },
	}
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	current := exchange.Order{
		ExchangeOrderID: "known-external-order", ClientOrderID: "known-external-client",
		Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(1_000),
	}
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	requireLogicalAccountAutomation(t, tradeStore, "PAUSED")
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")

	fill := exchange.Fill{
		ExchangeTradeID: "known-external-trade",
		ExchangeOrderID: current.ExchangeOrderID,
		ClientOrderID:   current.ClientOrderID,
		Symbol:          current.Symbol,
		Side:            current.Side,
		PositionSide:    current.PositionSide,
		Quantity:        shared.MustDecimal("0.25"),
		Price:           shared.MustDecimal("100"),
		SettlementAsset: "USDT",
		TradedAt:        time.UnixMilli(2_000),
	}
	applied, err := service.ApplyFill(context.Background(), "account-1", fill)
	require.NoError(t, err)
	require.True(t, applied)
	requireLogicalAccountAutomation(t, tradeStore, "PAUSED")

	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	applied, err = service.ApplyFill(context.Background(), "account-1", fill)
	require.NoError(t, err)
	require.False(t, applied)
	requireLogicalAccountRemainsActive(t, tradeStore)
}

func TestKnownExternalOrderUpdatePausesOnceForNewFact(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	observer := runFactsObserver(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Facts: observer,
		Now:   func() time.Time { return time.UnixMilli(4_000) },
	}
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	current := exchange.Order{
		ExchangeOrderID: "updated-external-order",
		ClientOrderID:   "updated-external-client",
		Symbol:          "BTC-USDT",
		OrderType:       exchange.OrderTypeMarket,
		Side:            exchange.SideBuy,
		PositionSide:    exchange.PositionSideNet,
		Quantity:        shared.MustDecimal("1"),
		Status:          exchange.OrderStatusOpen,
		CreatedAt:       time.UnixMilli(1_000),
		UpdatedAt:       time.UnixMilli(1_000),
	}
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	requireLogicalAccountAutomation(t, tradeStore, "PAUSED")
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")

	current.UpdatedAt = time.UnixMilli(2_000)
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	requireLogicalAccountAutomation(t, tradeStore, "PAUSED")

	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	requireLogicalAccountRemainsActive(t, tradeStore)
}

func TestKnownExternalAggregateFillAheadPausesLogicalAccount(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	observer := runFactsObserver(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Facts: observer,
		Now:   func() time.Time { return time.UnixMilli(4_000) },
	}
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	current := exchange.Order{
		ExchangeOrderID: "aggregate-external-order",
		ClientOrderID:   "aggregate-external-client",
		Symbol:          "BTC-USDT",
		OrderType:       exchange.OrderTypeMarket,
		Side:            exchange.SideBuy,
		PositionSide:    exchange.PositionSideNet,
		Quantity:        shared.MustDecimal("1"),
		Status:          exchange.OrderStatusOpen,
		CreatedAt:       time.UnixMilli(1_000),
		UpdatedAt:       time.UnixMilli(1_000),
	}
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	requireLogicalAccountAutomation(t, tradeStore, "PAUSED")
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")

	current.FilledQuantity = shared.MustDecimal("0.5")
	current.Status = exchange.OrderStatusPartiallyFilled
	current.UpdatedAt = time.UnixMilli(2_000)
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	requireLogicalAccountAutomation(t, tradeStore, "PAUSED")
}

func TestTerminalExternalFillPausesSynchronouslyWithoutObserver(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Now:   func() time.Time { return time.UnixMilli(4_000) },
	}
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")
	current := exchange.Order{
		ExchangeOrderID: "terminal-external-order",
		ClientOrderID:   "terminal-external-client",
		Symbol:          "BTC-USDT",
		OrderType:       exchange.OrderTypeMarket,
		Side:            exchange.SideBuy,
		PositionSide:    exchange.PositionSideNet,
		Quantity:        shared.MustDecimal("1"),
		Status:          exchange.OrderStatusOpen,
		CreatedAt:       time.UnixMilli(1_000),
		UpdatedAt:       time.UnixMilli(1_000),
	}
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	requireLogicalAccountAutomation(t, tradeStore, "PAUSED")
	setLogicalAccountAutomation(t, tradeStore, "ACTIVE")

	applied, err := service.ApplyFill(context.Background(), "account-1", exchange.Fill{
		ExchangeTradeID: "terminal-external-trade",
		ExchangeOrderID: current.ExchangeOrderID,
		ClientOrderID:   current.ClientOrderID,
		Symbol:          current.Symbol,
		Side:            current.Side,
		PositionSide:    current.PositionSide,
		Quantity:        shared.MustDecimal("1"),
		Price:           shared.MustDecimal("100"),
		SettlementAsset: "USDT",
		TradedAt:        time.UnixMilli(2_000),
	})
	require.NoError(t, err)
	require.True(t, applied)
	logicalAccount, err := tradeStore.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logicalAccount.AutomationState)
	order, err := tradeStore.GetOrderByClientID(
		context.Background(), "space-1", "account-1", current.ClientOrderID,
	)
	require.NoError(t, err)
	require.Equal(t, "FILLED", order.State)
}

func TestAccountFactsWakeTargetWorkerAfterReleasingAccountLock(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	woke := make(chan struct{}, 1)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Facts: &LogicalAccountFactsObserver{
			Store: tradeStore,
			Wake: func() {
				unlock := tradeStore.LockTradingAccount("account-1")
				unlock()
				woke <- struct{}{}
			},
		},
		Now: func() time.Time { return time.UnixMilli(3_000) },
	}
	current := exchange.Order{
		ExchangeOrderID: "external-order", ClientOrderID: "outside-client",
		Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(2_000), UpdatedAt: time.UnixMilli(2_000),
	}
	done := make(chan error, 1)
	go func() {
		done <- service.ApplyOrder(context.Background(), "account-1", current)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("AccountFacts callback ran while the TradingAccount lock was held")
	}
	select {
	case <-woke:
	case <-time.After(time.Second):
		t.Fatal("TargetWorker was not woken after account facts changed")
	}
}

func TestExternalFactsDoNotReenterLogicalAccountLock(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	observer := runFactsObserver(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Facts: observer,
		Now:   func() time.Time { return time.UnixMilli(3_000) },
	}
	unlock := tradeStore.LockLogicalAccount("space-1", "logical-1")
	done := make(chan error, 1)
	go func() {
		done <- service.ApplyOrder(context.Background(), "account-1", exchange.Order{
			ExchangeOrderID: "external-order", ClientOrderID: "outside-client",
			Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
			CreatedAt: time.UnixMilli(2_000), UpdatedAt: time.UnixMilli(2_000),
		})
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("AccountSync re-entered a LogicalAccount lock held by its caller")
	}
	unlock()
	require.Eventually(t, func() bool {
		logicalAccount, err := tradeStore.GetLogicalAccount(
			context.Background(), "space-1", "logical-1",
		)
		return err == nil && logicalAccount.AutomationState == "PAUSED"
	}, time.Second, 10*time.Millisecond)
}

func TestOrderReducerIgnoresOlderExchangeUpdateAndAggregateFill(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Now:   func() time.Time { return time.UnixMilli(3_000) },
	}
	open := exchange.Order{
		ExchangeOrderID: "exchange-monotonic", ClientOrderID: "client-monotonic",
		Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(2_000), UpdatedAt: time.UnixMilli(2_000),
	}
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", open))

	aggregateWithoutFill := open
	aggregateWithoutFill.Status = exchange.OrderStatusFilled
	aggregateWithoutFill.FilledQuantity = shared.MustDecimal("1")
	aggregateWithoutFill.UpdatedAt = time.UnixMilli(3_000)
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", aggregateWithoutFill))
	olderTerminal := open
	olderTerminal.Status = exchange.OrderStatusRejected
	olderTerminal.UpdatedAt = time.UnixMilli(1_000)
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", olderTerminal))

	record, err := tradeStore.GetOrderByClientID(
		context.Background(), "space-1", "account-1", open.ClientOrderID,
	)
	require.NoError(t, err)
	require.Equal(t, "OPEN", record.State)
	require.Equal(t, "0", record.FilledQuantity)
	require.Equal(t, int64(3_000), record.ExchangeUpdatedAt)
}

func TestOrderReducerDoesNotRegressTerminalState(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Now:   func() time.Time { return time.UnixMilli(4_000) },
	}
	current := exchange.Order{
		ExchangeOrderID: "exchange-terminal", ClientOrderID: "client-terminal",
		Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(2_000), UpdatedAt: time.UnixMilli(2_000),
	}
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	current.Status = exchange.OrderStatusRejected
	current.UpdatedAt = time.UnixMilli(3_000)
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	current.Status = exchange.OrderStatusOpen
	current.UpdatedAt = time.UnixMilli(4_000)
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))

	record, err := tradeStore.GetOrderByClientID(
		context.Background(), "space-1", "account-1", current.ClientOrderID,
	)
	require.NoError(t, err)
	require.Equal(t, "REJECTED", record.State)
}

func TestOrderReducerIgnoresRegressingCumulativeFillSnapshot(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Now:   func() time.Time { return time.UnixMilli(5_000) },
	}
	current := exchange.Order{
		ExchangeOrderID: "exchange-fill-regression", ClientOrderID: "client-fill-regression",
		Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("2"), Status: exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
	}
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", current))
	record, err := tradeStore.GetOrderByClientID(
		context.Background(), "space-1", "account-1", current.ClientOrderID,
	)
	require.NoError(t, err)
	expected := record.Version
	record.FilledQuantity = "1"
	record.State = "PARTIALLY_FILLED"
	record.ExchangeUpdatedAt = 3_000
	record.Version++
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}))

	regressing := current
	regressing.Status = exchange.OrderStatusRejected
	regressing.FilledQuantity = shared.MustDecimal("0.5")
	regressing.UpdatedAt = time.UnixMilli(4_000)
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", regressing))

	record, err = tradeStore.GetOrderByClientID(
		context.Background(), "space-1", "account-1", current.ClientOrderID,
	)
	require.NoError(t, err)
	require.Equal(t, "PARTIALLY_FILLED", record.State)
	require.Equal(t, "1", record.FilledQuantity)
	require.Equal(t, int64(3_000), record.ExchangeUpdatedAt)
}

func TestApplySnapshotResolvesUnknownWithoutReenteringAccountLock(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	now := time.UnixMilli(3_000)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "unknown-order",
			TradingAccountID: "account-1", ClientOrderID: "unknown-client",
			Symbol: "BTC-USDT", OrderType: "MARKET", Side: "BUY",
			PositionSide: "NET", Quantity: "1", ReferencePrice: "100",
			ReferencePriceAt: 1_000, OwnerType: "TARGET", OwnerID: "target-1",
			LogicalAccountID: "logical-1", RunnerID: "runner-1",
			State:          "SUBMIT_UNKNOWN",
			FilledQuantity: "0", ReservedAsset: "USDT", ReservedQuantity: "100",
			RemainingReservedQuantity: "100", Version: 1, SubmittedAt: 1_000,
		})
	}))
	adapter := &syncAdapter{order: exchange.Order{
		ExchangeOrderID: "exchange-unknown", ClientOrderID: "unknown-client",
		Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
	}}
	adapters := syncAdapterSource{adapter: adapter}
	orders := &orderapp.Service{
		Store: tradeStore, Adapters: adapters, Now: func() time.Time { return now },
	}
	service := Service{
		Store: tradeStore, Adapters: adapters, Fills: &consumer.Reducer{Store: tradeStore},
		Orders: orders, Now: func() time.Time { return now },
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.ApplySnapshot(context.Background(), "account-1", Snapshot{
			Account: exchange.AccountSnapshot{ExchangeUpdatedAt: time.UnixMilli(2_000)},
		})
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("ApplySnapshot deadlocked while resolving an unknown order")
	}
}

func TestPaperSyncRecoversSubmittedOrderAndPersistsSpotFill(t *testing.T) {
	ctx := context.Background()
	tradeStore := openSyncStore(t)
	submittedAt := time.Now().Add(-time.Second).UTC().UnixMilli()
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "paper-1", Name: "paper",
			Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER",
			Environment:     "PAPER",
			SettlementAsset: "USDT", Status: "ENABLED",
			SyncSymbols: []string{"BTCUSDT"},
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			InstrumentID: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", ExchangeQuantityStep: "0.0001",
			PriceTick: "0.01", Status: "TRADING",
		}); err != nil {
			return err
		}
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "order-1",
			TradingAccountID: "paper-1", ClientOrderID: "client-1",
			Symbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
			Quantity: "0.01", ReferencePrice: "50000",
			ReferencePriceAt: submittedAt,
			OwnerType:        "EXTERNAL", OwnerID: "paper-existing",
			State: "SUBMITTING", FilledQuantity: "0",
			ReservedAsset: "USDT", ReservedQuantity: "500",
			RemainingReservedQuantity: "500", Version: 1,
			SubmittedAt: submittedAt,
		})
	}))

	adapter := paper.New(
		&paperPublicAdapter{syncAdapter: &syncAdapter{}},
		tradeStore,
		"space-1",
		"paper-1",
		exchange.MarketTypeSpot,
		"USDT",
		shared.MustDecimal("100000"),
		exchange.MarginModeUnspecified,
		nil,
	)
	_, err := adapter.LoadInstruments(ctx)
	require.NoError(t, err)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
	}

	result, err := service.SyncAccount(ctx, "paper-1")
	require.NoError(t, err)
	require.Equal(t, 1, result.FillsIngested)

	orderRecord, err := tradeStore.GetOrder(ctx, "space-1", "order-1")
	require.NoError(t, err)
	require.Equal(t, "FILLED", orderRecord.State)
	require.NotEmpty(t, orderRecord.ExchangeOrderID)
	fills, total, err := tradeStore.ListFills(ctx, "space-1", store.FillQuery{
		TradingAccountID: "paper-1",
		Limit:            10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, fills, 1)
	require.Empty(t, fills[0].PositionSide)
}

func TestApplyPartialPositionUsesConfiguredLeverage(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	err := service.ApplyPosition(context.Background(), "account-1", exchange.Position{
		TradingAccountID:  "account-1",
		Symbol:            "BTC-USDT",
		PositionSide:      exchange.PositionSideNet,
		SignedQuantity:    shared.MustDecimal("1"),
		EntryPrice:        shared.MustDecimal("100"),
		MarginMode:        exchange.MarginModeCross,
		UnrealizedPnL:     shared.MustDecimal("2"),
		ExchangeUpdatedAt: time.UnixMilli(2_000),
		Present: exchange.PositionPresence{
			SignedQuantity: true,
			EntryPrice:     true,
			MarginMode:     true,
			UnrealizedPnL:  true,
		},
		RequiresSync: true,
	})
	require.NoError(t, err)
	var position store.PositionRecord
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		var found bool
		var getErr error
		position, found, getErr = tx.GetPosition(
			"space-1",
			"account-1",
			"BTC-USDT",
			"NET",
		)
		require.True(t, found)
		return getErr
	}))
	require.Equal(t, "5", position.Leverage)
	require.Equal(t, "1", position.SignedQuantity)
}

func TestPartialPositionAndAccountPushDoNotWakeTargetBeforeFullSync(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	wakes := 0
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
		Facts: &LogicalAccountFactsObserver{
			Store: tradeStore,
			Wake:  func() { wakes++ },
		},
	}
	require.NoError(t, service.ApplyPosition(
		context.Background(), "account-1", exchange.Position{
			Symbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
			SignedQuantity:    shared.MustDecimal("1"),
			EntryPrice:        shared.MustDecimal("100"),
			MarginMode:        exchange.MarginModeCross,
			ExchangeUpdatedAt: time.UnixMilli(2_500),
			Present: exchange.PositionPresence{
				SignedQuantity: true, EntryPrice: true, MarginMode: true,
			},
			RequiresSync: true,
		},
	))
	require.NoError(t, service.ApplyAccountSnapshot(
		context.Background(), "account-1", exchange.AccountSnapshot{
			AvailableFunds:    shared.MustDecimal("800"),
			ExchangeUpdatedAt: time.UnixMilli(2_500),
			Present: exchange.AccountSnapshotPresence{
				AvailableFunds: true,
			},
			RequiresSync: true,
		},
	))
	require.Zero(t, wakes)
	account, err := tradeStore.GetTradingAccountByID(
		context.Background(), "account-1",
	)
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.Contains(t, account.LastError, "awaiting full sync")
}

func TestApplyUnknownPartialPositionDefersToFullSync(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync(
			"space-1",
			"account-1",
			store.TradingAccountSyncState{LastSyncAt: 1},
		)
	}))
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	require.NoError(t, service.ApplyPosition(context.Background(), "account-1", exchange.Position{
		Symbol: "ETH-USDT", PositionSide: exchange.PositionSideNet,
		SignedQuantity: shared.MustDecimal("1"),
		EntryPrice:     shared.MustDecimal("100"), MarginMode: exchange.MarginModeCross,
		ExchangeUpdatedAt: time.UnixMilli(2_000),
		Present: exchange.PositionPresence{
			SignedQuantity: true, EntryPrice: true, MarginMode: true,
		},
		RequiresSync: true,
	}))
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		_, found, err := tx.GetPosition("space-1", "account-1", "ETH-USDT", "NET")
		require.False(t, found)
		return err
	}))
}

func TestApplyFillImportsEmptyClientIDPerSymbol(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpdateTradingAccountSync(
			"space-1",
			"account-1",
			store.TradingAccountSyncState{
				LeverageSettings: store.LeverageSettings{
					"BTC-USDT": "5",
					"ETH-USDT": "5",
				},
				LastSyncAt: 1,
			},
		); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SWAP", Symbol: "ETH-USDT",
			InstrumentID: "ETHUSDT", BaseAsset: "ETH", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "0.1",
			ContractValueAsset: "ETH", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	adapter := &syncAdapter{order: exchange.Order{
		OrderType: exchange.OrderTypeMarket,
		Side:      exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(1_000),
	}}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	for index, symbol := range []string{"BTC-USDT", "ETH-USDT"} {
		applied, err := service.ApplyFill(context.Background(), "account-1", exchange.Fill{
			ExchangeTradeID: fmt.Sprintf("trade-%d", index),
			ExchangeOrderID: "shared-exchange-id",
			Symbol:          symbol,
			Side:            exchange.SideBuy,
			PositionSide:    exchange.PositionSideNet,
			Quantity:        shared.MustDecimal("0.1"),
			Price:           shared.MustDecimal("100"),
			SettlementAsset: "USDT",
			TradedAt:        time.UnixMilli(2_000 + int64(index)),
		})
		require.NoError(t, err)
		require.True(t, applied)
	}
	first, err := tradeStore.GetOrderByExchangeID(
		context.Background(), "space-1", "account-1", "BTC-USDT", "shared-exchange-id",
	)
	require.NoError(t, err)
	second, err := tradeStore.GetOrderByExchangeID(
		context.Background(), "space-1", "account-1", "ETH-USDT", "shared-exchange-id",
	)
	require.NoError(t, err)
	require.NotEqual(t, first.OrderID, second.OrderID)
	require.NotEqual(t, first.ClientOrderID, second.ClientOrderID)
}

func TestApplySnapshotAggregatesSyntheticExternalOrderFills(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{lookupErr: &exchange.Error{
		Kind: exchange.ErrorOrderNotFound,
	}}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	fills := []exchange.Fill{
		{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "archived-order",
			Symbol: "BTC-USDT", Side: exchange.SideBuy,
			PositionSide: exchange.PositionSideNet,
			Quantity:     shared.MustDecimal("0.2"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_000),
		},
		{
			ExchangeTradeID: "trade-2", ExchangeOrderID: "archived-order",
			Symbol: "BTC-USDT", Side: exchange.SideBuy,
			PositionSide: exchange.PositionSideNet,
			Quantity:     shared.MustDecimal("0.3"), Price: shared.MustDecimal("110"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_100),
		},
	}
	result, err := service.ApplySnapshot(context.Background(), "account-1", Snapshot{
		Fills: fills,
		Account: exchange.AccountSnapshot{
			ExchangeUpdatedAt: time.UnixMilli(2_000),
		},
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.FillsIngested)
	order, err := tradeStore.GetOrderByExchangeID(
		context.Background(),
		"space-1",
		"account-1",
		"BTC-USDT",
		"archived-order",
	)
	require.NoError(t, err)
	require.Equal(t, "0.5", order.Quantity)
	require.Equal(t, "0.5", order.FilledQuantity)
	require.Equal(t, "FILLED", order.State)
}

func TestMergePrivateSnapshotUsesPresenceAndPreservesUnmentionedBalances(t *testing.T) {
	current := store.TradingAccountSnapshot{
		Balances: []store.AssetBalance{
			{Asset: "BTC", Available: "1", Total: "1"},
			{Asset: "USDT", Available: "100", Total: "100"},
		},
		AvailableFunds:    "100",
		ExchangeUpdatedAt: 1_000,
	}
	merged := mergePrivateSnapshot(current, exchange.AccountSnapshot{
		Balances: []exchange.AssetBalance{{
			Asset: "BTC", Available: shared.Zero(), Total: shared.Zero(),
		}},
		AvailableFunds:    shared.Zero(),
		ExchangeUpdatedAt: time.UnixMilli(2_000),
		Present:           exchange.AccountSnapshotPresence{Balances: true},
	})
	require.Equal(t, "100", merged.AvailableFunds)
	require.Len(t, merged.Balances, 2)
	require.Equal(t, "0", merged.Balances[0].Available)
	require.Equal(t, "USDT", merged.Balances[1].Asset)

	merged = mergePrivateSnapshot(merged, exchange.AccountSnapshot{
		AvailableFunds:    shared.Zero(),
		ExchangeUpdatedAt: time.UnixMilli(3_000),
		Present: exchange.AccountSnapshotPresence{
			AvailableFunds: true,
		},
	})
	require.Equal(t, "0", merged.AvailableFunds)
}

func TestApplyAccountSnapshotMergesDisjointBalancesAtSameTimestamp(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store:    tradeStore,
		Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills:    &consumer.Reducer{Store: tradeStore},
	}
	updatedAt := time.UnixMilli(2_000)

	require.NoError(t, service.ApplyAccountSnapshot(
		context.Background(),
		"account-1",
		exchange.AccountSnapshot{
			Balances: []exchange.AssetBalance{{
				Asset: "BTC", Available: shared.MustDecimal("1"),
				Total: shared.MustDecimal("1"),
			}},
			ExchangeUpdatedAt: updatedAt,
			Present:           exchange.AccountSnapshotPresence{Balances: true},
		},
	))
	require.NoError(t, service.ApplyAccountSnapshot(
		context.Background(),
		"account-1",
		exchange.AccountSnapshot{
			Balances: []exchange.AssetBalance{{
				Asset: "USDT", Available: shared.MustDecimal("100"),
				Total: shared.MustDecimal("100"),
			}},
			ExchangeUpdatedAt: updatedAt,
			Present:           exchange.AccountSnapshotPresence{Balances: true},
		},
	))

	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, int64(2_000), account.Snapshot.ExchangeUpdatedAt)
	require.Len(t, account.Snapshot.Balances, 2)
	require.Equal(t, "BTC", account.Snapshot.Balances[0].Asset)
	require.Equal(t, "USDT", account.Snapshot.Balances[1].Asset)
}

func TestPrivateSnapshotDoesNotAdvanceFullSnapshotWatermark(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountFacts(
			"space-1",
			"account-1",
			nil,
			store.TradingAccountSnapshot{
				AvailableFunds: "900", ExchangeUpdatedAt: 2_000,
			},
			2_000,
			3_000,
		)
	}))
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	require.NoError(t, service.ApplyAccountSnapshot(
		context.Background(),
		"account-1",
		exchange.AccountSnapshot{
			Balances: []exchange.AssetBalance{{
				Asset: "USDT", Available: shared.MustDecimal("800"),
				Total: shared.MustDecimal("800"),
			}},
			ExchangeUpdatedAt: time.UnixMilli(2_500),
			Present:           exchange.AccountSnapshotPresence{Balances: true},
			RequiresSync:      true,
		},
	))
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, int64(3_000), account.LastSyncAt)
	require.Equal(t, int64(2_000), account.SnapshotSourceTime)
	require.Equal(t, int64(2_500), account.Snapshot.ExchangeUpdatedAt)
}

func TestDisconnectReadinessWinsAgainstConcurrentManualSync(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{
		order: exchange.Order{
			ExchangeOrderID: "exchange-1", ClientOrderID: "manual-1",
			Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
		},
		fill: exchange.Fill{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "exchange-1",
			ClientOrderID: "manual-1", Symbol: "BTC-USDT", Side: exchange.SideBuy,
			PositionSide: exchange.PositionSideNet,
			Quantity:     shared.MustDecimal("0.1"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_500),
		},
		fillStarted: make(chan struct{}),
		fillRelease: make(chan struct{}),
	}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(3_000) },
	}
	syncDone := make(chan error, 1)
	go func() {
		_, err := service.SyncAccount(context.Background(), "account-1")
		syncDone <- err
	}()
	<-adapter.fillStarted
	disconnectDone := make(chan error, 1)
	go func() {
		disconnectDone <- service.SetReady(
			context.Background(),
			"account-1",
			false,
			errors.New("disconnected"),
		)
	}()
	select {
	case err := <-disconnectDone:
		t.Fatalf("disconnect update bypassed account synchronization: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(adapter.fillRelease)
	require.NoError(t, <-syncDone)
	require.NoError(t, <-disconnectDone)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.Equal(t, "disconnected", account.LastError)
}

func TestSyncSymbolsRetainsConfiguredSpotSymbolAfterSellToZero(t *testing.T) {
	symbols := syncSymbols(
		store.TradingAccountRecord{
			MarketType:  "SPOT",
			SyncSymbols: []string{"BTC-USDT"},
		},
		nil,
		nil,
		nil,
		[]store.InstrumentRecord{{
			Symbol: "BTC-USDT", BaseAsset: "BTC", SettlementAsset: "USDT",
			Status: "TRADING",
		}},
		exchange.AccountSnapshot{Balances: []exchange.AssetBalance{{
			Asset: "BTC", Total: shared.Zero(),
		}}},
	)
	require.Equal(t, []string{"BTC-USDT"}, symbols)

	symbols = syncSymbols(
		store.TradingAccountRecord{MarketType: "SWAP"},
		nil,
		nil,
		[]exchange.Position{{Symbol: "ETH-USDT"}},
		nil,
		exchange.AccountSnapshot{},
	)
	require.Equal(t, []string{"ETH-USDT"}, symbols)
}

func openSyncStore(t *testing.T) *store.Store {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	return tradeStore
}

func runFactsObserver(
	t *testing.T,
	tradeStore *store.Store,
) *LogicalAccountFactsObserver {
	t.Helper()
	observer := &LogicalAccountFactsObserver{
		Store: tradeStore, RetryInterval: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	})
	require.Eventually(t, func() bool {
		return observer.Snapshot().Ready
	}, time.Second, 10*time.Millisecond)
	return observer
}

func setLogicalAccountAutomation(
	t *testing.T,
	tradeStore *store.Store,
	state string,
) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", state, "")
	}))
}

func requireLogicalAccountAutomation(
	t *testing.T,
	tradeStore *store.Store,
	state string,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		logicalAccount, err := tradeStore.GetLogicalAccount(
			context.Background(), "space-1", "logical-1",
		)
		return err == nil && logicalAccount.AutomationState == state
	}, time.Second, 10*time.Millisecond)
}

func requireLogicalAccountRemainsActive(
	t *testing.T,
	tradeStore *store.Store,
) {
	t.Helper()
	require.Never(t, func() bool {
		logicalAccount, err := tradeStore.GetLogicalAccount(
			context.Background(), "space-1", "logical-1",
		)
		return err != nil || logicalAccount.AutomationState != "ACTIVE"
	}, 100*time.Millisecond, 10*time.Millisecond)
}

func seedSyncAccount(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "main",
			Exchange: "BINANCE", MarketType: "SWAP", ExecutionMode: "LIVE",
			Environment:        "TESTNET",
			CredentialSecretID: "secret-1", SettlementAsset: "USDT",
			MarginMode: "CROSS", Status: "ENABLED",
			LeverageSettings: store.LeverageSettings{"BTC-USDT": "5"},
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", ExecutionMode: "LIVE",
			MarketType: "SWAP", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "test",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "account-1", Enabled: true,
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", Environment: "TESTNET", MarketType: "SWAP", Symbol: "BTC-USDT",
			InstrumentID: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "0.1",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
}
