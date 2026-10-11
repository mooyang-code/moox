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
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type syncAdapterSource struct {
	adapter execution.ExecutionAdapter
}

type readySessionState bool

func (s readySessionState) Ready(string) bool { return bool(s) }

func (s syncAdapterSource) Adapter(string) (execution.ExecutionAdapter, error) {
	return s.adapter, nil
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
		ExchangeSymbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
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
	shared.ExchangeSymbol,
	string,
) ([]exchange.Fill, string, error) {
	if a.fillStarted != nil {
		close(a.fillStarted)
		<-a.fillRelease
	}
	return []exchange.Fill{a.fill}, "11", nil
}
func (a *syncAdapter) GetOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
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
	current.ExchangeSymbol = symbol
	current.ExchangeOrderID = exchangeOrderID
	return current, nil
}
func (*syncAdapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*syncAdapter) CancelOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	return exchange.Order{}, nil
}
func (*syncAdapter) SetLeverage(context.Context, shared.ExchangeSymbol, shared.Decimal) error {
	return nil
}
func (*syncAdapter) SetMarginMode(context.Context, shared.ExchangeSymbol, exchange.MarginMode) error {
	return nil
}
func (*syncAdapter) Subscribe(_ context.Context, handler execution.AccountEventHandler) error {
	handler.OnSubscribed()
	return nil
}

func TestServiceSyncAccountImportsExternalOrderAndAppliesFacts(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{
		order: exchange.Order{
			ExchangeOrderID: "exchange-1", ClientOrderID: "manual-1",
			ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.5"),
			AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusPartiallyFilled,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
		},
		fill: exchange.Fill{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "exchange-1",
			ClientOrderID: "manual-1", ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy,
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
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
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
		ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy,
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
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
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
		ExchangeSymbol:  current.ExchangeSymbol,
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
		ExchangeSymbol:  "BTC-USDT",
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
		ExchangeSymbol:  "BTC-USDT",
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
		ExchangeSymbol:  "BTC-USDT",
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
		ExchangeSymbol:  current.ExchangeSymbol,
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
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
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
			ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
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
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
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
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
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
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
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
			ExchangeSymbol: "BTC-USDT", OrderType: "MARKET", Side: "BUY",
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
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
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

func TestApplyPartialPositionUsesConfiguredLeverage(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}},
		Fills: &consumer.Reducer{Store: tradeStore},
	}
	err := service.ApplyPosition(context.Background(), "account-1", exchange.Position{
		TradingAccountID:  "account-1",
		ExchangeSymbol:    "BTC-USDT",
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
			ExchangeSymbol: "BTC-USDT", PositionSide: exchange.PositionSideNet,
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
		ExchangeSymbol: "ETH-USDT", PositionSide: exchange.PositionSideNet,
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
			Exchange: "BINANCE", Environment: "TESTNET", MarketType: "SWAP", ExchangeSymbol: "ETH-USDT",
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
			ExchangeSymbol:  symbol,
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
			ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy,
			PositionSide: exchange.PositionSideNet,
			Quantity:     shared.MustDecimal("0.2"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_000),
		},
		{
			ExchangeTradeID: "trade-2", ExchangeOrderID: "archived-order",
			ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy,
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
			ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), Status: exchange.OrderStatusOpen,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
		},
		fill: exchange.Fill{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "exchange-1",
			ClientOrderID: "manual-1", ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy,
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
			ExchangeSymbol: "BTC-USDT", BaseAsset: "BTC", SettlementAsset: "USDT",
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
		[]exchange.Position{{ExchangeSymbol: "ETH-USDT"}},
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
			Exchange: "BINANCE", Environment: "TESTNET", MarketType: "SWAP", ExchangeSymbol: "BTC-USDT",
			InstrumentID: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "0.1",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
}

type staleSnapshotAdapter struct{ *syncAdapter }

func (staleSnapshotAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{
		AvailableFunds:    shared.MustDecimal("999"),
		ExchangeUpdatedAt: time.UnixMilli(1_000), // 早于已采用的快照（2_000）
	}, nil
}

// LastSyncAt 是资金预留的抵扣边界：丢弃陈旧余额快照时不能推进它，否则已提交订单的预留会被误认为已反映在余额中。
func TestStaleSnapshotDoesNotAdvanceReservationBoundary(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	adapter := &syncAdapter{order: exchange.Order{
		ExchangeOrderID: "exchange-1", ClientOrderID: "manual-1", ExchangeSymbol: "BTC-USDT",
		OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), FilledQuantity: shared.Zero(), Status: exchange.OrderStatusPartiallyFilled,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
	}, fill: exchange.Fill{
		ExchangeTradeID: "trade-1", ExchangeOrderID: "exchange-1", ClientOrderID: "manual-1",
		ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.5"), Price: shared.MustDecimal("100"),
		SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_500),
	}}
	clock := int64(3_000)
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: adapter},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(clock) },
	}
	_, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)

	clock = 5_000
	service.Adapters = syncAdapterSource{adapter: staleSnapshotAdapter{adapter}}
	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, result.AccountSnapshotUpdated)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, int64(3_000), account.LastSyncAt, "陈旧快照被丢弃时抵扣边界保持不变")
	require.Equal(t, int64(2_000), account.SnapshotSourceTime)
}

// 实盘私有成交已释放订单预留，但余额快照尚未反映这笔支出：成交后必须关闭准入，直到完整同步确认余额。
func TestPrivateFillClosesAdmissionUntilFullSync(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	current := exchange.Order{
		ExchangeOrderID: "order-1", ClientOrderID: "client-1",
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.25"),
		AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusPartiallyFilled,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
	}
	fill := exchange.Fill{
		ExchangeTradeID: "trade-1", ExchangeOrderID: "order-1", ClientOrderID: "client-1",
		ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.25"), Price: shared.MustDecimal("100"),
		SettlementAsset: "USDT", TradedAt: time.UnixMilli(2_000),
	}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{order: current, fill: fill}},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(4_000) },
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountReadiness("space-1", "account-1", true, 3_000, "")
	}))
	open := current
	open.FilledQuantity, open.Status = shared.Zero(), exchange.OrderStatusOpen
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", open))
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.True(t, account.Ready, "事件前账户就绪")

	applied, err := service.ApplyFill(context.Background(), "account-1", fill)
	require.NoError(t, err)
	require.True(t, applied)
	account, err = tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready, "成交后在余额同步确认前不得接受新订单")

	_, err = service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	account, err = tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.True(t, account.Ready, "完整同步确认余额后恢复")
}

type recordingMetrics struct{ errs []error }

func (m *recordingMetrics) Observe(_ string, _ time.Time, _ float64, err error) {
	m.errs = append(m.errs, err)
}

type snapshotAtAdapter struct {
	*syncAdapter
	at int64
}

func (a snapshotAtAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{
		AvailableFunds:    shared.MustDecimal("900"),
		Equity:            shared.MustDecimal("1000"),
		ExchangeUpdatedAt: time.UnixMilli(a.at),
	}, nil
}

// 成交后必须等到覆盖成交时间的完整余额快照才恢复准入：旧于成交、与已采用快照同时间、介于两者之间的快照都不行。
func TestAdmissionStaysClosedUntilBalanceSnapshotCoversPrivateFill(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	current := exchange.Order{
		ExchangeOrderID: "order-1", ClientOrderID: "client-1",
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.25"),
		AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusPartiallyFilled,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
	}
	fill := exchange.Fill{
		ExchangeTradeID: "trade-1", ExchangeOrderID: "order-1", ClientOrderID: "client-1",
		ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.25"), Price: shared.MustDecimal("100"),
		SettlementAsset: "USDT", TradedAt: time.UnixMilli(2_000),
	}
	base := &syncAdapter{order: current, fill: fill}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: base},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(4_000) },
	}
	_, err := service.SyncAccount(context.Background(), "account-1") // 余额快照 2_000 已采用
	require.NoError(t, err)

	newFill := fill
	newFill.ExchangeTradeID, newFill.TradedAt = "trade-2", time.UnixMilli(3_000)
	applied, err := service.ApplyFill(context.Background(), "account-1", newFill)
	require.NoError(t, err)
	require.True(t, applied)
	boundary := func() int64 {
		account, getErr := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
		require.NoError(t, getErr)
		return account.LastSyncAt
	}
	before := boundary()

	metrics := &recordingMetrics{}
	service.Metrics = metrics
	for _, at := range []int64{1_500, 2_000, 2_900} { // 旧于已采用快照 / 与已采用快照同时间 / 介于旧余额与成交之间
		service.Adapters = syncAdapterSource{adapter: snapshotAtAdapter{base, at}}
		service.Now = func() time.Time { return time.UnixMilli(5_000 + at) }
		result, syncErr := service.SyncAccount(context.Background(), "account-1")
		require.NoError(t, syncErr)
		require.False(t, result.Ready, "快照时间 %d 未覆盖成交 3_000", at)
		require.True(t, result.BalancePending)
		account, getErr := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
		require.NoError(t, getErr)
		require.False(t, account.Ready)
		require.Equal(t, int64(3_000), account.BalancePendingUntil)
		require.Equal(t, before, boundary(), "未覆盖成交的快照不能推进预留抵扣边界")
	}

	require.Len(t, metrics.errs, 3)
	for _, observed := range metrics.errs {
		require.Error(t, observed, "余额未确认时监控必须看到失败，而不是同步新鲜")
	}
	service.Adapters = syncAdapterSource{adapter: snapshotAtAdapter{base, 3_000}}
	service.Now = func() time.Time { return time.UnixMilli(9_000) }
	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.True(t, result.Ready)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.True(t, account.Ready, "覆盖成交的快照恢复准入")
	require.Zero(t, account.BalancePendingUntil)
	require.Equal(t, int64(9_000), account.LastSyncAt)
}

// 私有余额更新（2_500）之后收到更旧的 REST 完整快照（2_200）：旧快照不能覆盖更新的字段，也不能完成余额确认。
func TestOlderRESTSnapshotDoesNotOverwritePrivateBalanceUpdate(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	base := &syncAdapter{
		order: exchange.Order{
			ExchangeOrderID: "order-1", ClientOrderID: "client-1",
			ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.25"),
			AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusPartiallyFilled,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
		},
		fill: exchange.Fill{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "order-1", ClientOrderID: "client-1",
			ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("0.25"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(2_000),
		},
	}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: base},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(4_000) },
	}
	_, err := service.SyncAccount(context.Background(), "account-1") // 完整快照 2_000，可用资金 900
	require.NoError(t, err)

	private := exchange.AccountSnapshot{
		AvailableFunds: shared.MustDecimal("100"), ExchangeUpdatedAt: time.UnixMilli(2_500),
		Present: exchange.AccountSnapshotPresence{AvailableFunds: true},
	}
	require.NoError(t, service.ApplyAccountSnapshot(context.Background(), "account-1", private))

	service.Adapters = syncAdapterSource{adapter: snapshotAtAdapter{base, 2_200}}
	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, result.AccountSnapshotUpdated)
	require.False(t, result.Ready)
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.Equal(t, "100", account.Snapshot.AvailableFunds, "更旧的 REST 不得把可用资金改回 900")
}

// REST 首次补录的成交同样会释放预留：同一轮读取的余额若早于成交，不能恢复准入；水位在成交入账的同一事务内抬高。
func TestRESTBackfilledFillRaisesBalanceWatermarkAtomically(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	base := &syncAdapter{
		order: exchange.Order{
			ExchangeOrderID: "order-1", ClientOrderID: "client-1",
			ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.25"),
			AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusPartiallyFilled,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(3_000),
		},
		fill: exchange.Fill{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "order-1", ClientOrderID: "client-1",
			ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("0.25"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(3_000),
		},
	}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: snapshotAtAdapter{base, 2_000}},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(4_000) },
	}
	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, 1, result.FillsIngested)
	require.False(t, result.Ready, "成交之前读到的余额不能恢复准入")
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.Equal(t, int64(3_000), account.BalancePendingUntil)

	// 重放同一成交（已去重）不会丢水位，余额覆盖成交后恢复
	service.Adapters = syncAdapterSource{adapter: snapshotAtAdapter{base, 3_000}}
	result, err = service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.True(t, result.Ready)
	account, err = tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.Zero(t, account.BalancePendingUntil)
}

type noPositionsAdapter struct{ *syncAdapter }

func (noPositionsAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, nil
}

// 持仓列表先于成交读取、余额快照后于成交读取：本轮补录成交投影出的持仓不能被“空持仓列表”删除。
func TestFullSyncKeepsPositionProjectedFromFillBackfilledInTheSameRound(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	base := &syncAdapter{
		order: exchange.Order{
			ExchangeOrderID: "order-1", ClientOrderID: "client-1",
			ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.5"),
			AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusPartiallyFilled,
			CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(1_500),
		},
		fill: exchange.Fill{
			ExchangeTradeID: "trade-1", ExchangeOrderID: "order-1", ClientOrderID: "client-1",
			ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("0.5"), Price: shared.MustDecimal("100"),
			SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_500),
		},
	}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: noPositionsAdapter{base}},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(4_000) },
	}
	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, 1, result.FillsIngested)
	var positions int64
	require.NoError(t, tradeStore.DBForTest().Raw(
		"SELECT COUNT(*) FROM t_trading_positions WHERE c_space_id = ? AND c_trading_account_id = ?", "space-1", "account-1",
	).Scan(&positions).Error)
	require.Equal(t, int64(1), positions, "本轮补录成交投影出的持仓不能被先取的空持仓列表删除")
}

// 账户锁被长时间占用（例如下单正在做网络调用）时，带期限的入口（断线收尾的 SetReady、私有事件处理）必须按期限返回，
// 而不是无限等锁：断线时给 SetReady 设置的 2 秒期限靠它才能真正生效。
func TestAccountLockWaitHonorsCallerDeadline(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	service := Service{Store: tradeStore, Adapters: syncAdapterSource{adapter: &syncAdapter{}}, Fills: &consumer.Reducer{Store: tradeStore}}
	unlock := tradeStore.LockTradingAccount("account-1")
	defer unlock()

	for name, call := range map[string]func(context.Context) error{
		"SetReady": func(ctx context.Context) error { return service.SetReady(ctx, "account-1", false, nil) },
		"ApplyPosition": func(ctx context.Context) error {
			return service.ApplyPosition(ctx, "account-1", exchange.Position{ExchangeSymbol: "BTC-USDT", PositionSide: exchange.PositionSideNet, ExchangeUpdatedAt: time.UnixMilli(1_000)})
		},
		"ApplyAccountSnapshot": func(ctx context.Context) error {
			return service.ApplyAccountSnapshot(ctx, "account-1", exchange.AccountSnapshot{ExchangeUpdatedAt: time.UnixMilli(1_000)})
		},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- call(ctx) }()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.DeadlineExceeded, name)
		case <-time.After(2 * time.Second):
			t.Fatalf("%s kept waiting for the trading account lock past the deadline", name)
		}
		cancel()
	}
}

// 终态订单更新先于成交明细到达：订单累计成交领先本地明细时，终态转换会释放预留，但余额还没有反映这些成交——
// 必须先关闭准入，直到完整同步补录明细且余额覆盖该时间。
func TestOrderAheadOfFillDetailsClosesAdmissionUntilBalanceCovers(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	open := exchange.Order{
		ExchangeOrderID: "order-1", ClientOrderID: "client-1",
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), FilledQuantity: shared.Zero(),
		Status:    exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(1_000),
	}
	base := &syncAdapter{order: open}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: base},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(4_000) },
	}
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", open))
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountReadiness("space-1", "account-1", true, 3_000, "")
	}))

	// 撤单更新显示已成交 0.5，但本地还没有任何成交明细
	canceled := open
	canceled.FilledQuantity, canceled.Status, canceled.UpdatedAt = shared.MustDecimal("0.5"), exchange.OrderStatusCanceled, time.UnixMilli(3_000)
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", canceled))
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready, "订单累计成交领先明细时不得保持可交易")
	require.Equal(t, int64(3_000), account.BalancePendingUntil)
}

type noFillsAdapter struct {
	*syncAdapter
	at int64
}

func (a noFillsAdapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	return snapshotAtAdapter{a.syncAdapter, a.at}.GetAccountSnapshot(ctx)
}

func (noFillsAdapter) ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error) {
	return nil, "", nil
}

// 订单累计成交领先本地明细时，更新的余额快照也不能恢复准入（余额没有被缺失的成交明细解释）：
// 明细补齐且余额覆盖水位之后才恢复。
func TestFillGapKeepsAdmissionClosedEvenWhenBalanceIsNewer(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	open := exchange.Order{
		ExchangeOrderID: "order-1", ClientOrderID: "client-1",
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), FilledQuantity: shared.Zero(),
		Status:    exchange.OrderStatusOpen,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(1_000),
	}
	canceled := open
	canceled.FilledQuantity, canceled.AveragePrice = shared.MustDecimal("0.5"), shared.MustDecimal("100")
	canceled.Status, canceled.UpdatedAt = exchange.OrderStatusPartiallyFilled, time.UnixMilli(3_000)
	fill := exchange.Fill{
		ExchangeTradeID: "trade-1", ExchangeOrderID: "order-1", ClientOrderID: "client-1",
		ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.5"), Price: shared.MustDecimal("100"),
		SettlementAsset: "USDT", TradedAt: time.UnixMilli(2_900),
	}
	base := &syncAdapter{order: canceled, fill: fill}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: noFillsAdapter{base, 4_000}},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(5_000) },
	}
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", open))
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", canceled)) // 累计成交 0.5 领先明细（0）

	// 余额（4_000）比订单更新（3_000）新，但缺失的成交明细还没有返回
	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, result.Ready, "成交明细仍缺失时不能恢复准入")
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready)
	require.NotZero(t, account.BalancePendingUntil)

	// 成交明细补齐后再同步：恢复
	service.Adapters = syncAdapterSource{adapter: snapshotAtAdapter{base, 4_000}}
	service.Now = func() time.Time { return time.UnixMilli(6_000) }
	result, err = service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, 1, result.FillsIngested)
	require.True(t, result.Ready, "明细补齐且余额覆盖水位后恢复")
}

type lateFillAdapter struct {
	*syncAdapter
	fill  exchange.Fill
	order exchange.Order
}

func (lateFillAdapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, nil
}
func (a lateFillAdapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return []exchange.Order{a.order}, nil
}
func (a lateFillAdapter) ListRecentFills(context.Context, shared.ExchangeSymbol, string) ([]exchange.Fill, string, error) {
	return []exchange.Fill{a.fill}, "12", nil
}

// 权威（REST）持仓快照已覆盖到的时间之前的迟到成交，数量已包含在快照里：不能再叠加到持仓上（成交仍入账本）。
func TestLateFillCoveredByPositionSnapshotDoesNotDoubleCountPosition(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	order := exchange.Order{
		ExchangeOrderID: "order-1", ClientOrderID: "client-1",
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.5"),
		AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusPartiallyFilled,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(2_000),
	}
	fill1 := exchange.Fill{
		ExchangeTradeID: "trade-1", ExchangeOrderID: "order-1", ClientOrderID: "client-1",
		ExchangeSymbol: "BTC-USDT", Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.5"), Price: shared.MustDecimal("100"),
		SettlementAsset: "USDT", TradedAt: time.UnixMilli(1_500),
	}
	base := &syncAdapter{order: order, fill: fill1}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: base}, // 持仓快照：数量 0.5，版本时间 2_000
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(4_000) },
	}
	_, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)

	// 迟到的成交（时间 1_800 < 快照覆盖时间 2_000）已包含在持仓快照里
	late := fill1
	late.ExchangeTradeID, late.Quantity, late.TradedAt = "trade-2", shared.MustDecimal("0.25"), time.UnixMilli(1_800)
	orderLate := order
	orderLate.FilledQuantity = shared.MustDecimal("0.75")
	service.Adapters = syncAdapterSource{adapter: lateFillAdapter{base, late, orderLate}}
	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, 1, result.FillsIngested, "迟到成交仍然入成交账本")
	var quantity string
	require.NoError(t, tradeStore.DBForTest().Raw(
		"SELECT c_signed_quantity FROM t_trading_positions WHERE c_space_id = ? AND c_trading_account_id = ? AND c_exchange_symbol = ?",
		"space-1", "account-1", "BTC-USDT",
	).Scan(&quantity).Error)
	require.Equal(t, "0.5", quantity, "已被权威快照覆盖的迟到成交不能重复叠加持仓")
}

// 首次导入就是终态且累计成交领先本地明细的外部订单：导入入口同样必须关闭准入（水位原先为零，缺口检查不能依赖水位）。
func TestFirstImportedTerminalOrderWithMissingFillsKeepsAdmissionClosed(t *testing.T) {
	tradeStore := openSyncStore(t)
	seedSyncAccount(t, tradeStore)
	imported := exchange.Order{
		ExchangeOrderID: "ext-1", ClientOrderID: "ext-client-1",
		ExchangeSymbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("1"), FilledQuantity: shared.MustDecimal("0.5"),
		AveragePrice: shared.MustDecimal("100"), Status: exchange.OrderStatusCanceled,
		CreatedAt: time.UnixMilli(1_000), UpdatedAt: time.UnixMilli(3_000),
	}
	base := &syncAdapter{order: imported}
	service := Service{
		Store: tradeStore, Adapters: syncAdapterSource{adapter: noFillsAdapter{base, 4_000}},
		SessionState: readySessionState(true),
		Fills:        &consumer.Reducer{Store: tradeStore},
		Now:          func() time.Time { return time.UnixMilli(5_000) },
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountReadiness("space-1", "account-1", true, 2_000, "")
	}))
	require.NoError(t, service.ApplyOrder(context.Background(), "account-1", imported))
	account, err := tradeStore.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, account.Ready, "首次导入的终态外部订单缺成交明细时必须关闭准入")

	// 余额（4_000）比订单更新新，但明细仍缺失：完整同步也不能恢复
	result, err := service.SyncAccount(context.Background(), "account-1")
	require.NoError(t, err)
	require.False(t, result.Ready)
}
