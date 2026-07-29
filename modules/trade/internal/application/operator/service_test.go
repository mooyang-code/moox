package operator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestManualOrderPausesAndCancelsTargetsBeforeSubmit(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "target-child",
		ExchangeAccountID: "account-a", ClientOrderID: "target-child",
		Symbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
		PositionSide: "NET", Quantity: "1", ReferencePrice: "100",
		ReferencePriceAt: fixture.now.UnixMilli(),
		OwnerType:        "TARGET", OwnerID: "target-1",
		LogicalAccountID: "logical-1", RunnerID: "runner-1",
		State: "OPEN", Version: 1,
	})

	result, err := fixture.service().PlaceManualOrder(
		context.Background(),
		ManualOrderCommand{
			SpaceID: "space-1", ActionID: "manual-1",
			ExchangeAccountID: "account-a", ClientOrderID: "manual-client",
			InstrumentID: "BTCUSDT", Type: exchange.OrderTypeMarket,
			Side: exchange.SideSell, PositionSide: exchange.PositionSideNet,
			Quantity: shared.MustDecimal("1"), Reason: "operator override",
		},
	)

	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.Equal(t, "manual-order-1", result.Order.OrderID)
	require.Equal(t, []string{
		"cancel:target-child",
		"sync:account-a",
		"place:manual-client",
		"submit:manual-order-1",
	}, fixture.trace)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, orderdomain.OwnerOperator, fixture.orders.specs[0].Owner.Type)
	require.Equal(t, "manual-1", fixture.orders.specs[0].Owner.OwnerID)
	require.Equal(t, "logical-1", fixture.orders.specs[0].Owner.LogicalAccountID)
	require.Nil(t, fixture.orders.specs[0].Owner.RunnerID)
	account, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
	require.Equal(t, "operator override", account.PauseReason)
}

func TestManualOrderActionIDIsIdempotent(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := ManualOrderCommand{
		SpaceID: "space-1", ActionID: "manual-1",
		ExchangeAccountID: "account-a", ClientOrderID: "manual-client",
		InstrumentID: "BTCUSDT", Type: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.1"),
		Reason: "operator override",
	}

	first, err := fixture.service().PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	replayed, err := fixture.service().PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)

	require.Equal(t, first.Action.ActionID, replayed.Action.ActionID)
	require.Equal(t, first.Order.OrderID, replayed.Order.OrderID)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, 1, fixture.orders.submitCalls)

	command.Quantity = shared.MustDecimal("0.2")
	_, err = fixture.service().PlaceManualOrder(context.Background(), command)
	require.ErrorIs(t, err, store.ErrConflict)
}

func TestManualOrderDoesNotCancelNonTargetOrders(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "external",
		ExchangeAccountID: "account-a", ClientOrderID: "external",
		ExchangeOrderID: "exchange-1", Symbol: "BTCUSDT",
		OrderType: "MARKET", Side: "SELL", Quantity: "0.1",
		ReferencePrice: "100", ReferencePriceAt: fixture.now.UnixMilli(),
		OwnerType: "EXTERNAL", OwnerID: "exchange-1",
		LogicalAccountID: "logical-1", State: "OPEN", Version: 1,
	})

	_, err := fixture.service().PlaceManualOrder(
		context.Background(),
		ManualOrderCommand{
			SpaceID: "space-1", ActionID: "manual-1",
			ExchangeAccountID: "account-a", ClientOrderID: "manual-client",
			InstrumentID: "BTCUSDT", Type: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.1"),
			Reason: "operator override",
		},
	)

	require.NoError(t, err)
	require.NotContains(t, fixture.trace, "cancel:external")
}

func TestManualOrderRejectsTargetDiscoveredDuringCancellationSync(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	fixture.order(t, activeOrder(fixture, "target-child", "TARGET"))
	fixture.syncer.onSync = func(ctx context.Context, accountID string, call int) error {
		if accountID != "account-a" || call != 1 {
			return nil
		}
		fixture.order(t, store.OrderRecord{
			SpaceID: "space-1", OrderID: "late-target",
			ExchangeAccountID: "account-a", ClientOrderID: "late-target",
			Symbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
			Quantity: "0.1", ReferencePrice: "100",
			ReferencePriceAt: fixture.now.UnixMilli(),
			OwnerType:        "TARGET", OwnerID: "target-1",
			LogicalAccountID: "logical-1", RunnerID: "runner-1",
			State: "OPEN", Version: 1,
		})
		return nil
	}

	_, err := fixture.service().PlaceManualOrder(
		context.Background(),
		ManualOrderCommand{
			SpaceID: "space-1", ActionID: "manual-1",
			ExchangeAccountID: "account-a", ClientOrderID: "manual-client",
			InstrumentID: "BTCUSDT", Type: exchange.OrderTypeMarket,
			Side: exchange.SideBuy, Quantity: shared.MustDecimal("0.1"),
			Reason: "operator override",
		},
	)

	require.ErrorIs(t, err, ErrCancelUnconfirmed)
	require.Empty(t, fixture.orders.specs)
}

func TestCancelOrderPausesLogicalAccountAndIsIdempotent(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.order(t, activeOrder(fixture, "target-child", "TARGET"))
	command := CancelOrderCommand{
		SpaceID: "space-1", ActionID: "cancel-1",
		OrderID: "target-child", Reason: "operator cancel",
	}

	first, err := fixture.service().CancelOrder(context.Background(), command)
	require.NoError(t, err)
	replayed, err := fixture.service().CancelOrder(context.Background(), command)
	require.NoError(t, err)

	require.Equal(t, "COMPLETED", first.Action.Status)
	require.Equal(t, first.Order.OrderID, replayed.Order.OrderID)
	require.Equal(t, []string{
		"cancel:target-child", "sync:account-a",
	}, fixture.trace)
	account, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
	require.Equal(t, "operator cancel", account.PauseReason)
	orderRecord, err := fixture.store.GetOrder(
		context.Background(), "space-1", "target-child",
	)
	require.NoError(t, err)
	require.Equal(t, "TARGET", orderRecord.OwnerType)
	require.Equal(t, "owner-1", orderRecord.OwnerID)

	command.OrderID = "other"
	_, err = fixture.service().CancelOrder(context.Background(), command)
	require.ErrorIs(t, err, store.ErrConflict)
}

func TestResumeOperatorActionContinuesPersistedCancel(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.order(t, activeOrder(fixture, "target-child", "TARGET"))
	requestJSON, err := cancelOrderRequestJSON(CancelOrderCommand{
		SpaceID: "space-1", ActionID: "cancel-1",
		OrderID: "target-child", Reason: "operator cancel",
	})
	require.NoError(t, err)
	action, _, err := fixture.store.CreateOperatorAction(
		context.Background(),
		store.OperatorActionRecord{
			SpaceID: "space-1", ActionID: "cancel-1",
			LogicalAccountID: "logical-1", ActionType: "CANCEL_ORDER",
			Reason: "operator cancel", RequestJSON: requestJSON, Status: "RUNNING",
		},
	)
	require.NoError(t, err)

	require.NoError(t, fixture.service().ResumeOperatorAction(
		context.Background(), action,
	))

	current, err := fixture.store.GetOperatorAction(
		context.Background(), "space-1", "cancel-1",
	)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", current.Status)
	require.Equal(t, []string{
		"cancel:target-child", "sync:account-a",
	}, fixture.trace)
}

type operatorFixture struct {
	t      *testing.T
	store  *store.Store
	orders *operatorOrderStub
	syncer *operatorSyncStub
	prices operatorPriceStub
	trace  []string
	now    time.Time
	market exchange.MarketType
}

func newOperatorFixture(t *testing.T, market exchange.MarketType) *operatorFixture {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	fixture := &operatorFixture{
		t: t, store: tradeStore, now: time.UnixMilli(2_000).UTC(),
		market: market,
		prices: operatorPriceStub{
			quote: Quote{
				Price: shared.MustDecimal("100"), UpdatedAt: time.UnixMilli(2_000).UTC(),
			},
		},
	}
	fixture.orders = &operatorOrderStub{
		fixture: fixture, store: tradeStore, nextID: "manual-order-1",
	}
	fixture.syncer = &operatorSyncStub{fixture: fixture}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, accountID := range []string{"account-a", "account-b"} {
			if err := tx.CreateExchangeAccount(fixture.account(accountID)); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", ExecutionMode: "PAPER",
			MarketType: string(market), SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		for index, accountID := range []string{"account-a", "account-b"} {
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-1",
				ExchangeAccountID: accountID, Enabled: true, Priority: index + 1,
			}); err != nil {
				return err
			}
		}
		for _, accountID := range []string{"account-a", "account-b"} {
			if err := tx.UpsertInstrument(fixture.instrument(accountID)); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "ACTIVE", "",
		)
	}))
	return fixture
}

func (f *operatorFixture) account(id string) store.ExchangeAccountRecord {
	exchangeName := "BINANCE"
	if id == "account-b" {
		exchangeName = "OKX"
	}
	return store.ExchangeAccountRecord{
		SpaceID: "space-1", ExchangeAccountID: id, Name: id,
		Exchange: exchangeName, MarketType: string(f.market),
		ExecutionMode: "PAPER", Environment: "PAPER",
		SettlementAsset: "USDT", MarginMode: map[bool]string{
			true: "CROSS", false: "",
		}[f.market == exchange.MarketTypeSwap],
		Status: "ENABLED", Ready: true,
		LeverageSettings: store.LeverageSettings{
			"BTCUSDT": "5", "BTC-USDT-SWAP": "5",
		},
		Snapshot: store.ExchangeAccountSnapshot{
			AvailableFunds: "100000",
			Balances: []store.AssetBalance{{
				Asset: "USDT", Available: "100000", Total: "100000",
			}},
		},
		LastSyncAt: f.now.UnixMilli(), SnapshotSourceTime: f.now.UnixMilli(),
	}
}

func (f *operatorFixture) instrument(accountID string) store.InstrumentRecord {
	exchangeName := "BINANCE"
	symbol := "BTCUSDT"
	if accountID == "account-b" {
		exchangeName = "OKX"
		symbol = "BTC-USDT-SWAP"
	}
	instrument := store.InstrumentRecord{
		Exchange: exchangeName, MarketType: string(f.market), Symbol: symbol,
		InstrumentID: "BTC-USDT-" + string(f.market),
		BaseAsset:    "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
		ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
		PriceTick: "0.1", MinNotional: "5", Status: "TRADING",
		ExchangeUpdatedAt: f.now.UnixMilli(),
	}
	if f.market == exchange.MarketTypeSwap {
		instrument.Linear = true
		instrument.ContractValue = "0.001"
		instrument.ContractValueAsset = "BTC"
		instrument.ExchangeQuantityStep = "1"
		instrument.MinExchangeQuantity = "1"
	}
	return instrument
}

func (f *operatorFixture) order(t *testing.T, record store.OrderRecord) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(record)
	}))
}

func (f *operatorFixture) position(t *testing.T, accountID, symbol, quantity string) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", ExchangeAccountID: accountID,
			Symbol: symbol, PositionSide: "NET", SignedQuantity: quantity,
			EntryPrice: "100", MarkPrice: "100", Leverage: "5",
			MarginMode: "CROSS", ExchangeUpdatedAt: f.now.UnixMilli(),
		})
	}))
}

func (f *operatorFixture) service() *Service {
	return &Service{
		Store: f.store, Orders: f.orders, Syncer: f.syncer, Prices: f.prices,
		Now: func() time.Time { return f.now },
	}
}

type operatorOrderStub struct {
	fixture     *operatorFixture
	store       *store.Store
	nextID      string
	specs       []orderdomain.OrderSpec
	submitCalls int
	leaveOpen   map[string]bool
}

func (s *operatorOrderStub) Place(
	ctx context.Context,
	spaceID string,
	spec orderdomain.OrderSpec,
) (orderdomain.Order, error) {
	s.fixture.trace = append(s.fixture.trace, "place:"+spec.ClientOrderID)
	s.specs = append(s.specs, spec)
	id := s.nextID
	if id == "" {
		id = "child-" + spec.ExchangeAccountID + "-" + spec.InstrumentID
	}
	record := store.OrderRecord{
		SpaceID: spaceID, OrderID: id,
		ExchangeAccountID: spec.ExchangeAccountID,
		ClientOrderID:     spec.ClientOrderID, Symbol: spec.InstrumentID,
		OrderType: string(spec.Type), TimeInForce: string(spec.FillPolicy),
		Side: string(spec.Side), PositionSide: string(spec.PositionSide),
		Quantity: spec.Quantity.String(), ReferencePrice: spec.ReferencePrice.String(),
		ReferencePriceAt: spec.ReferencePriceAt.UnixMilli(),
		ReduceOnly:       spec.ReducePositionOnly,
		OwnerType:        string(spec.Owner.Type), OwnerID: spec.Owner.OwnerID,
		LogicalAccountID: spec.Owner.LogicalAccountID,
		State:            "PENDING", Version: 1,
	}
	if spec.LimitPrice != nil {
		value := spec.LimitPrice.String()
		record.LimitPrice = &value
	}
	if spec.Owner.RunnerID != nil {
		record.RunnerID = *spec.Owner.RunnerID
	}
	if err := s.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(record)
	}); err != nil {
		return orderdomain.Order{}, err
	}
	return orderdomain.Order{
		ID: shared.OrderID(id), Spec: spec, State: orderdomain.Pending, Version: 1,
	}, nil
}

func (s *operatorOrderStub) Submit(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	s.fixture.trace = append(s.fixture.trace, "submit:"+orderID)
	s.submitCalls++
	if s.leaveOpen[orderID] {
		return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Open}, nil
	}
	if err := setOperatorOrderState(ctx, s.store, spaceID, orderID, "FILLED"); err != nil {
		return orderdomain.Order{}, err
	}
	return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Filled}, nil
}

func (s *operatorOrderStub) Cancel(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	s.fixture.trace = append(s.fixture.trace, "cancel:"+orderID)
	if s.leaveOpen[orderID] {
		return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Open}, nil
	}
	if err := setOperatorOrderState(ctx, s.store, spaceID, orderID, "CANCELED"); err != nil {
		return orderdomain.Order{}, err
	}
	return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Canceled}, nil
}

func (s *operatorOrderStub) DiscardPending(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	return s.Cancel(ctx, spaceID, orderID)
}

func (s *operatorOrderStub) ResolveUnknown(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	return s.Cancel(ctx, spaceID, orderID)
}

func (s *operatorOrderStub) RecoverCancel(
	ctx context.Context,
	spaceID string,
	orderID string,
) (orderdomain.Order, error) {
	return s.Cancel(ctx, spaceID, orderID)
}

func setOperatorOrderState(
	ctx context.Context,
	tradeStore *store.Store,
	spaceID string,
	orderID string,
	state string,
) error {
	record, err := tradeStore.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return err
	}
	expected := record.Version
	record.State = state
	record.Version++
	record.FinishedAt = time.Now().UnixMilli()
	return tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	})
}

type operatorSyncStub struct {
	fixture *operatorFixture
	fail    map[string]error
	onSync  func(context.Context, string, int) error
}

func (s *operatorSyncStub) SyncAccount(
	ctx context.Context,
	exchangeAccountID string,
) error {
	s.fixture.trace = append(s.fixture.trace, "sync:"+exchangeAccountID)
	if err := s.fail[exchangeAccountID]; err != nil {
		return err
	}
	if s.onSync == nil {
		return nil
	}
	return s.onSync(ctx, exchangeAccountID, s.callsFor(exchangeAccountID))
}

func (s *operatorSyncStub) callsFor(accountID string) int {
	count := 0
	for _, entry := range s.fixture.trace {
		if entry == "sync:"+accountID {
			count++
		}
	}
	return count
}

type operatorPriceStub struct {
	quote Quote
	err   error
}

func (s operatorPriceStub) LatestPrice(
	context.Context,
	string,
	string,
) (Quote, error) {
	return s.quote, s.err
}
