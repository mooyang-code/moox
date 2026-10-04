package test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/stretchr/testify/require"
)

func TestManualRefreshedPaperMarketUsesFrozenSlippageAtMatcher(t *testing.T) {
	ctx := context.Background()
	f := newProductionPaperFixture(t, exchange.MarketTypeSwap)
	seedSwapLogicalAccount(t, f.store)
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_paper_account_configs SET c_slippage_bps = '10' WHERE c_space_id = ? AND c_trading_account_id = ?", testSpace, testAccount).Error)
	now := testNow
	f.adapter.(recordingAdapter).ExecutionAdapter.(*paperexec.Adapter).Now = func() time.Time { return now }
	service := &operator.Service{Store: f.store, Orders: manualSubmitDispatchLoss{Service: f.orders}, Syncer: syncBridge{service: f.sync}, Prices: logicalAccountE2EPriceSource{at: testNow}, Now: func() time.Time { return now }, ManualSubmitWindow: 2 * time.Minute}
	command := operator.ManualOrderCommand{SpaceID: testSpace, ActionID: "paper-refresh", TradingAccountID: testAccount, ClientOrderID: "paper-refresh", InstrumentID: testInstrumentID, Type: exchange.OrderTypeMarket, Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet, Quantity: shared.MustDecimal("0.01"), Reason: "paper delayed submission"}
	initial, err := service.PlaceManualOrder(ctx, command)
	require.ErrorContains(t, err, "fault after action link before Submit dispatch")
	require.NotNil(t, initial.Order.PaperExecutionPrice)
	originalPrice := shared.MustDecimal(*initial.Order.PaperExecutionPrice)
	now = testNow.Add(70 * time.Second)
	f.orders.Now = func() time.Time { return now }
	f.orders.Validator.Now = func() time.Time { return now }
	f.fake.reference.Price = f.fake.reference.Price.Mul(shared.MustDecimal("2"))
	f.fake.reference.UpdatedAt = now
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_paper_account_configs SET c_slippage_bps = '100' WHERE c_space_id = ? AND c_trading_account_id = ?", testSpace, testAccount).Error)
	service.Orders = f.orders
	result, err := service.PlaceManualOrder(ctx, command)
	require.NoError(t, err)
	wantPrice := originalPrice.Mul(shared.MustDecimal("2"))
	require.Equal(t, wantPrice.String(), *result.Order.PaperExecutionPrice)
	productionPaperMatch(t, f)
	filled, err := f.store.GetOrder(ctx, testSpace, result.Order.OrderID)
	require.NoError(t, err)
	require.Equal(t, "FILLED", filled.State)
	require.Equal(t, wantPrice.String(), filled.AveragePrice)
	require.Equal(t, "0", filled.RemainingReservedQuantity)
	account, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	pnl := f.fake.reference.Price.Sub(wantPrice).Mul(command.Quantity)
	margin := f.fake.reference.Price.Mul(command.Quantity).Div(shared.MustDecimal("10"))
	require.Equal(t, shared.MustDecimal("100000").Add(pnl).Sub(margin).String(), account.Snapshot.AvailableFunds)
}

func TestManualUnknownSubmissionRemainsRecoverable(t *testing.T) {
	ctx := context.Background()
	fake := newFakeExchange(exchange.MarketTypeSwap)
	fake.placeErr = transportError("manual response lost")
	f := newFixture(t, exchange.MarketTypeSwap, fake)
	seedSwapLogicalAccount(t, f.store)
	now := testNow
	const submitWindow = 2 * time.Minute
	service := &operator.Service{
		Store: f.store, Orders: f.orders,
		Syncer: syncBridge{service: f.sync},
		Prices: logicalAccountE2EPriceSource{at: testNow},
		Now:    func() time.Time { return now }, ManualSubmitWindow: submitWindow,
	}
	command := operator.ManualOrderCommand{
		SpaceID: testSpace, ActionID: "manual-unknown-recovery",
		TradingAccountID: testAccount, ClientOrderID: "manual-stable-client",
		InstrumentID: testInstrumentID, Type: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.01"), Reason: "manual recovery test",
	}
	initial, err := service.PlaceManualOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", initial.Action.Status)
	require.NotEmpty(t, initial.Action.LastError)
	action, err := f.store.GetOperatorAction(ctx, testSpace, command.ActionID)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", action.Status, "response loss must remain recoverable")
	unknown, err := f.store.GetOrderByClientID(ctx, testSpace, testAccount, command.ClientOrderID)
	require.NoError(t, err)
	require.Equal(t, string(orderdomain.SubmitUnknown), unknown.State)
	require.NotNil(t, action.ResultJSON)
	require.Contains(t, *action.ResultJSON, unknown.OrderID)
	var progress struct {
		DeadlineAt int64 `json:"deadline_at"`
	}
	require.NoError(t, json.Unmarshal([]byte(*action.ResultJSON), &progress))
	require.Equal(t, testNow.Add(submitWindow).UnixMilli(), progress.DeadlineAt)

	fake.mu.Lock()
	fake.placeErr = nil
	fake.orders[command.ClientOrderID] = exchange.Order{
		ExchangeOrderID: "accepted-manual-response-lost", ClientOrderID: command.ClientOrderID,
		ExchangeSymbol: testSymbol, OrderType: command.Type, Side: command.Side,
		PositionSide: command.PositionSide, Quantity: command.Quantity,
		Status: exchange.OrderStatusOpen, CreatedAt: testNow, UpdatedAt: testNow,
	}
	fake.mu.Unlock()
	// Expiry prohibits new submission, not querying an already submitted child.
	now = testNow.Add(submitWindow + time.Second)
	f.orders.Now = func() time.Time { return now }
	restarted := *service
	service = &restarted
	runManualRecoveryWorker(t, f, service)
	replayed, err := service.PlaceManualOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", replayed.Action.Status)
	require.Equal(t, unknown.OrderID, replayed.Order.OrderID)
	require.Equal(t, command.ClientOrderID, replayed.Order.ClientOrderID)
	require.Equal(t, string(orderdomain.Open), replayed.Order.State)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Equal(t, 1, fake.placeCalls, "recovery must never place a replacement")
	require.Positive(t, fake.lookupCalls, "recovery must query the existing client ID")
}

func runManualRecoveryWorker(t *testing.T, f *fixture, service *operator.Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	worker := &traderuntime.OperatorWorker{
		Actions: f.store, Resumer: service, Interval: time.Hour,
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	defer func() {
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	}()
	require.Eventually(t, func() bool { return worker.Snapshot().Ready }, 3*time.Second, time.Millisecond)
	require.Empty(t, worker.Snapshot().LastError)
}

type manualAckSyncFailure struct {
	calls int
}

type manualImmediateFillExchange struct {
	*fakeExchange
}

func (f manualImmediateFillExchange) PlaceOrder(ctx context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	response, err := f.fakeExchange.PlaceOrder(ctx, request)
	if err == nil {
		response.Status = exchange.OrderStatusFilled
		response.FilledQuantity = response.Quantity
	}
	return response, err
}

func (s *manualAckSyncFailure) SyncAccount(context.Context, string) error {
	s.calls++
	return errors.New("account sync failed after acknowledged submission")
}

func TestManualAcknowledgedSubmissionCompletesDespiteSyncFailure(t *testing.T) {
	ctx := context.Background()
	f, service, command := manualRecoveryFixture(t)
	failure := &manualAckSyncFailure{}
	f.orders.Syncer = failure
	f.orders.Adapters = adapterSource{adapter: manualImmediateFillExchange{fakeExchange: f.fake}}
	result, err := service.PlaceManualOrder(ctx, command)
	require.ErrorContains(t, err, "account sync failed after acknowledged submission")
	require.Equal(t, 1, failure.calls, "exercise the actual OrderService post-ACK sync failure")
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.Empty(t, result.Action.LastError)
	require.Equal(t, string(orderdomain.Open), result.Order.State)
	require.NotEmpty(t, result.Order.ExchangeOrderID)
	runManualRecoveryWorker(t, f, service)
	replayed, err := service.PlaceManualOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, result.Order.OrderID, replayed.Order.OrderID)
	require.Equal(t, 1, failure.calls)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Equal(t, 1, f.fake.placeCalls)
}

type manualPlaceAcknowledgmentLoss struct {
	operator.OrderService
}

type manualSubmitDispatchLoss struct {
	*orderapp.Service
}

func (s manualSubmitDispatchLoss) Submit(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	pending, err := s.Service.Get(ctx, spaceID, orderID)
	if err != nil {
		return pending, err
	}
	return pending, errors.New("fault after action link before Submit dispatch")
}

func TestManualLinkedPendingChildRecoveredBeforeSubmit(t *testing.T) {
	ctx := context.Background()
	f, service, command := manualRecoveryFixture(t)
	service.Orders = manualSubmitDispatchLoss{Service: f.orders}
	initial, err := service.PlaceManualOrder(ctx, command)
	require.ErrorContains(t, err, "fault after action link before Submit dispatch")
	require.Equal(t, "RUNNING", initial.Action.Status)
	child, err := f.store.GetOrderByClientID(ctx, testSpace, testAccount, command.ClientOrderID)
	require.NoError(t, err)
	require.Equal(t, string(orderdomain.Pending), child.State)
	require.NotNil(t, initial.Action.ResultJSON)
	require.Contains(t, *initial.Action.ResultJSON, child.OrderID)
	f.fake.mu.Lock()
	require.Zero(t, f.fake.placeCalls)
	f.fake.mu.Unlock()

	restarted := *service
	restarted.Orders = f.orders
	runManualRecoveryWorker(t, f, &restarted)
	result, err := restarted.PlaceManualOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.Equal(t, child.OrderID, result.Order.OrderID)
	require.Equal(t, command.ClientOrderID, result.Order.ClientOrderID)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Equal(t, 1, f.fake.placeCalls)
}

func TestManualPendingAccountNotReadyRemainsAccepted(t *testing.T) {
	ctx := context.Background()
	f, service, command := manualRecoveryFixture(t)
	service.Orders = manualSubmitDispatchLoss{Service: f.orders}
	initial, err := service.PlaceManualOrder(ctx, command)
	require.ErrorContains(t, err, "fault after action link before Submit dispatch")
	require.Equal(t, "RUNNING", initial.Action.Status)
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountReadiness(testSpace, testAccount, false, testNow.UnixMilli(), "sync temporarily unavailable")
	}))
	service.Orders = f.orders
	f.account.SessionState = readySession(false)
	result, err := service.PlaceManualOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
	require.NotEmpty(t, result.Action.LastError)
	require.Equal(t, string(orderdomain.Pending), result.Order.State)
	require.Zero(t, f.fake.placeCalls)
}

func TestManualInitialAccountNotReadyRemainsAccepted(t *testing.T) {
	f, service, command := manualRecoveryFixture(t)
	f.account.SessionState = readySession(false)
	result, err := service.PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
	require.NotEmpty(t, result.Action.LastError)
	f.account.SessionState = readySession(true)
	runManualRecoveryWorker(t, f, service)
	result, err = service.PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
}

func TestManualStaleQuoteRecoversWithoutNewAction(t *testing.T) {
	f, service, command := manualRecoveryFixture(t)
	service.Prices = logicalAccountE2EPriceSource{at: testNow.Add(-2 * time.Minute)}
	result, err := service.PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
	require.Contains(t, result.Action.LastError, "stale")
	require.Zero(t, f.fake.placeCalls)
	service.Prices = logicalAccountE2EPriceSource{at: testNow}
	runManualRecoveryWorker(t, f, service)
	result, err = service.PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.Equal(t, 1, f.fake.placeCalls)
}

func TestManualWorkerQueriesUnknownBeforeAbsentRetry(t *testing.T) {
	f, service, command := manualRecoveryFixture(t)
	f.fake.placeErr = transportError("response lost")
	initial, err := service.PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, string(orderdomain.SubmitUnknown), initial.Order.State)
	now := testNow.Add(2 * time.Second)
	f.orders.Now = func() time.Time { return now }
	f.fake.placeErr = nil
	f.fake.reference.Price = f.fake.reference.Price.Mul(shared.MustDecimal("2"))
	f.fake.reference.UpdatedAt = now
	restarted := *service
	restarted.Now = func() time.Time { return now }
	runManualRecoveryWorker(t, f, &restarted)
	result, err := restarted.PlaceManualOrder(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.Equal(t, initial.Order.OrderID, result.Order.OrderID)
	require.Equal(t, initial.Order.ReferencePrice, result.Order.ReferencePrice)
	require.Equal(t, initial.Order.ReferencePriceAt, result.Order.ReferencePriceAt)
	require.Positive(t, f.fake.lookupCalls)
	require.Equal(t, 2, f.fake.placeCalls)
}

func TestManualDelayedPendingRefreshesOnlyServerQuote(t *testing.T) {
	for _, tc := range []struct {
		name, factor string
		insufficient bool
	}{
		{name: "increase", factor: "2"}, {name: "decrease", factor: "0.5"}, {name: "insufficient-other-reservation", factor: "2", insufficient: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f, service, command := manualRecoveryFixture(t)
			service.Orders = manualSubmitDispatchLoss{Service: f.orders}
			initial, err := service.PlaceManualOrder(ctx, command)
			require.ErrorContains(t, err, "fault after action link before Submit dispatch")
			before := initial.Order
			reserved := shared.MustDecimal(before.ReservedQuantity)
			now := testNow.Add(70 * time.Second)
			service.Now = func() time.Time { return now }
			f.orders.Now = func() time.Time { return now }
			f.orders.Validator.Now = func() time.Time { return now }
			service.Orders = f.orders
			stale, err := service.PlaceManualOrder(ctx, command)
			require.NoError(t, err)
			require.Equal(t, "RUNNING", stale.Action.Status)
			require.Equal(t, string(orderdomain.Pending), stale.Order.State)
			require.Zero(t, f.fake.placeCalls)
			factor := shared.MustDecimal(tc.factor)
			f.fake.account.ExchangeUpdatedAt = now
			f.fake.reference.UpdatedAt = now
			if tc.insufficient {
				// Reserve real projected cash with another validated child rather
				// than changing a derived account snapshot.
				other := swapSpec("another-client", exchange.SideBuy, "19.99", false)
				other.ReferencePriceAt = now
				mustPlace(t, f, other)
			}
			f.fake.reference.Price = f.fake.reference.Price.Mul(factor)
			f.fake.reference.UpdatedAt = now
			if tc.insufficient {
				result, callErr := service.PlaceManualOrder(ctx, command)
				require.NoError(t, callErr)
				require.Equal(t, "RUNNING", result.Action.Status)
				require.Contains(t, result.Action.LastError, "insufficient")
				require.Equal(t, before.ReferencePrice, result.Order.ReferencePrice)
				require.Equal(t, before.ReferencePriceAt, result.Order.ReferencePriceAt)
				require.Equal(t, before.RemainingReservedQuantity, result.Order.RemainingReservedQuantity)
				require.Equal(t, string(orderdomain.Pending), result.Order.State)
				require.Zero(t, f.fake.placeCalls)
				require.NoError(t, service.ResumeOperatorAction(ctx, result.Action), "pending capacity rejection is an action-local outcome")
				return
			}
			runManualRecoveryWorker(t, f, service)
			result, err := service.PlaceManualOrder(ctx, command)
			require.NoError(t, err)
			require.Equal(t, "COMPLETED", result.Action.Status)
			require.Equal(t, before.OrderID, result.Order.OrderID)
			require.Equal(t, before.ClientOrderID, result.Order.ClientOrderID)
			require.Equal(t, before.Quantity, result.Order.Quantity)
			require.Equal(t, before.LimitPrice, result.Order.LimitPrice)
			require.Equal(t, f.fake.reference.Price.String(), result.Order.ReferencePrice)
			require.Equal(t, now.UnixMilli(), result.Order.ReferencePriceAt)
			require.Equal(t, reserved.Mul(factor).String(), result.Order.ReservedQuantity)
			require.Equal(t, result.Order.ReservedQuantity, result.Order.RemainingReservedQuantity)
			require.Equal(t, 1, f.fake.placeCalls)
		})
	}
}

func TestManualLinkedPendingCannotSubmitAfterMembershipRemoval(t *testing.T) {
	ctx := context.Background()
	f, service, command := manualRecoveryFixture(t)
	service.Orders = manualSubmitDispatchLoss{Service: f.orders}
	initial, err := service.PlaceManualOrder(ctx, command)
	require.ErrorContains(t, err, "fault after action link before Submit dispatch")
	require.Equal(t, "RUNNING", initial.Action.Status)
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.DeleteLogicalAccountMember(testSpace, initial.Action.LogicalAccountID, testAccount)
	}))
	restarted := *service
	restarted.Orders = f.orders
	result, err := restarted.PlaceManualOrder(ctx, command)
	require.Error(t, err)
	require.Equal(t, "FAILED", result.Action.Status)
	child, err := f.store.GetOrderByClientID(ctx, testSpace, testAccount, command.ClientOrderID)
	require.NoError(t, err)
	require.Equal(t, string(orderdomain.Canceled), child.State)
	require.Equal(t, "0", child.RemainingReservedQuantity)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Zero(t, f.fake.placeCalls)
}

func TestManualClientIDConflictTerminatesActionWithoutChangingExistingOrder(t *testing.T) {
	ctx := context.Background()
	f, service, command := manualRecoveryFixture(t)
	first, err := service.PlaceManualOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", first.Action.Status)
	command.ActionID = "conflicting-action"
	result, err := service.PlaceManualOrder(ctx, command)
	require.ErrorIs(t, err, orderapp.ErrIdempotencyConflict)
	action, err := f.store.GetOperatorAction(ctx, testSpace, command.ActionID)
	require.NoError(t, err)
	require.Equal(t, "FAILED", action.Status)
	require.Equal(t, "FAILED", result.Action.Status)
	current, err := f.store.GetOrder(ctx, testSpace, first.Order.OrderID)
	require.NoError(t, err)
	require.Equal(t, first.Order.State, current.State)
	require.Equal(t, first.Order.Version, current.Version)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Equal(t, 1, f.fake.placeCalls)
}

func (s manualPlaceAcknowledgmentLoss) Place(ctx context.Context, spaceID string, spec orderdomain.OrderSpec) (orderdomain.Order, error) {
	placed, err := s.OrderService.Place(ctx, spaceID, spec)
	if err != nil {
		return placed, err
	}
	return placed, errors.New("fault after durable Place before action link")
}

func TestManualPendingChildRecoveredWithoutActionLink(t *testing.T) {
	ctx := context.Background()
	f, service, command := manualRecoveryFixture(t)
	service.Orders = manualPlaceAcknowledgmentLoss{OrderService: f.orders}
	initial, err := service.PlaceManualOrder(ctx, command)
	require.ErrorContains(t, err, "fault after durable Place before action link")
	require.Equal(t, "RUNNING", initial.Action.Status)
	child, err := f.store.GetOrderByClientID(ctx, testSpace, testAccount, command.ClientOrderID)
	require.NoError(t, err)
	require.Equal(t, string(orderdomain.Pending), child.State)
	require.NotContains(t, *initial.Action.ResultJSON, child.OrderID)

	restarted := *service
	restarted.Orders = f.orders
	require.NoError(t, restarted.ResumeOperatorAction(ctx, initial.Action))
	result, err := restarted.PlaceManualOrder(ctx, command)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.Equal(t, child.OrderID, result.Order.OrderID)
	require.Equal(t, command.ClientOrderID, result.Order.ClientOrderID)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.Equal(t, 1, f.fake.placeCalls)
}

func TestManualConfirmedAbsentPendingRespectsOriginalDeadline(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "within-deadline"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f, service, command := manualRecoveryFixture(t)
			f.fake.placeErr = transportError("manual response lost")
			initial, err := service.PlaceManualOrder(ctx, command)
			require.NoError(t, err)
			child, err := f.store.GetOrderByClientID(ctx, testSpace, testAccount, command.ClientOrderID)
			require.NoError(t, err)
			now := testNow.Add(2 * time.Second)
			if expired {
				now = testNow.Add(3 * time.Minute)
			}
			f.orders.Now = func() time.Time { return now }
			pending, err := f.orders.ResolveUnknown(ctx, testSpace, child.OrderID)
			require.NoError(t, err)
			require.Equal(t, orderdomain.Pending, pending.State)
			f.fake.placeErr = nil
			restarted := *service
			restarted.Now = func() time.Time { return now }
			restarted.ManualSubmitWindow = 24 * time.Hour
			err = restarted.ResumeOperatorAction(ctx, initial.Action)
			require.NoError(t, err, "a durably recorded expiration does not fail worker health")
			result, err := restarted.PlaceManualOrder(ctx, command)
			persisted, getErr := f.store.GetOrderByClientID(ctx, testSpace, testAccount, command.ClientOrderID)
			require.NoError(t, getErr)
			require.Equal(t, child.OrderID, persisted.OrderID)
			f.fake.mu.Lock()
			defer f.fake.mu.Unlock()
			if expired {
				require.ErrorContains(t, err, "deadline")
				require.Equal(t, "FAILED", result.Action.Status)
				require.Equal(t, string(orderdomain.Canceled), persisted.State)
				require.Equal(t, "0", persisted.RemainingReservedQuantity)
				require.Equal(t, 1, f.fake.placeCalls)
			} else {
				require.NoError(t, err)
				require.Equal(t, "COMPLETED", result.Action.Status)
				require.Equal(t, string(orderdomain.Open), persisted.State)
				require.Equal(t, 2, f.fake.placeCalls)
			}
			var progress struct {
				DeadlineAt int64 `json:"deadline_at"`
			}
			require.NotNil(t, result.Action.ResultJSON)
			require.NoError(t, json.Unmarshal([]byte(*result.Action.ResultJSON), &progress))
			require.Equal(t, testNow.Add(2*time.Minute).UnixMilli(), progress.DeadlineAt)
		})
	}
}

func manualRecoveryFixture(t *testing.T) (*fixture, *operator.Service, operator.ManualOrderCommand) {
	t.Helper()
	f := newFixture(t, exchange.MarketTypeSwap, newFakeExchange(exchange.MarketTypeSwap))
	seedSwapLogicalAccount(t, f.store)
	service := &operator.Service{
		Store: f.store, Orders: f.orders, Syncer: syncBridge{service: f.sync},
		Prices: logicalAccountE2EPriceSource{at: testNow},
		Now:    func() time.Time { return testNow }, ManualSubmitWindow: 2 * time.Minute,
	}
	command := operator.ManualOrderCommand{
		SpaceID: testSpace, ActionID: "manual-pending-recovery",
		TradingAccountID: testAccount, ClientOrderID: "manual-stable-client",
		InstrumentID: testInstrumentID, Type: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, PositionSide: exchange.PositionSideNet,
		Quantity: shared.MustDecimal("0.01"), Reason: "manual recovery test",
	}
	return f, service, command
}
