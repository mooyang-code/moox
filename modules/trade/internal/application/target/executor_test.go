package target

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type targetStoreStub struct {
	targetMu    sync.Mutex
	execution   store.TargetExecutionRecord
	account     store.ExchangeAccountRecord
	instrument  store.InstrumentRecord
	position    store.PositionRecord
	hasPosition bool
	orders      []store.OrderRecord
	updates     []store.TargetExecutionRecord
}

func (s *targetStoreStub) LockTargetBinding(string, string) func() {
	s.targetMu.Lock()
	return s.targetMu.Unlock
}

func (s *targetStoreStub) GetTargetExecutionByBinding(
	context.Context,
	string,
	string,
) (store.TargetExecutionRecord, error) {
	return s.execution, nil
}

func (s *targetStoreStub) GetExchangeAccountByID(
	context.Context,
	string,
) (store.ExchangeAccountRecord, error) {
	return s.account, nil
}

func (s *targetStoreStub) GetInstrument(
	context.Context,
	string,
	string,
	string,
) (store.InstrumentRecord, error) {
	return s.instrument, nil
}

func (s *targetStoreStub) GetPosition(
	context.Context,
	string,
	string,
	string,
	string,
) (store.PositionRecord, bool, error) {
	return s.position, s.hasPosition, nil
}

func (s *targetStoreStub) ListOrdersForLane(
	context.Context,
	string,
	string,
	string,
) ([]store.OrderRecord, error) {
	return append([]store.OrderRecord(nil), s.orders...), nil
}

func (s *targetStoreStub) UpdateTargetExecutionState(
	_ context.Context,
	record store.TargetExecutionRecord,
) (bool, error) {
	s.updates = append(s.updates, record)
	s.execution = record
	return true, nil
}

type targetOrderServiceStub struct {
	placed    []orderdomain.OrderSpec
	submitted []string
	canceled  []string
	discarded []string
	resolved  []string
	placeHook func()
}

func (s *targetOrderServiceStub) Place(
	_ context.Context,
	_ string,
	spec orderdomain.OrderSpec,
) (orderdomain.Order, error) {
	if s.placeHook != nil {
		s.placeHook()
	}
	s.placed = append(s.placed, spec)
	return orderdomain.Order{ID: shared.OrderID("child-order")}, nil
}

func (s *targetOrderServiceStub) Submit(
	_ context.Context,
	_ string,
	orderID string,
) (orderdomain.Order, error) {
	s.submitted = append(s.submitted, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}

func (s *targetOrderServiceStub) Cancel(
	_ context.Context,
	_ string,
	orderID string,
) (orderdomain.Order, error) {
	s.canceled = append(s.canceled, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}

func (s *targetOrderServiceStub) DiscardPending(
	_ context.Context,
	_ string,
	orderID string,
) (orderdomain.Order, error) {
	s.discarded = append(s.discarded, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}

func (s *targetOrderServiceStub) ResolveUnknown(
	_ context.Context,
	_ string,
	orderID string,
) (orderdomain.Order, error) {
	s.resolved = append(s.resolved, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}

type priceSourceStub struct {
	quote Quote
	err   error
}

func (s priceSourceStub) LatestPrice(context.Context, string, string) (Quote, error) {
	return s.quote, s.err
}

func TestConvergeIncludesSameDirectionOpenRemaining(t *testing.T) {
	executor, state, orders := newSpotExecutor("3", "1")
	state.orders = []store.OrderRecord{activeOrderRecord(
		"open-buy",
		exchange.SideBuy,
		"1",
		"0.25",
	)}

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusRunning, result.Status)
	require.Empty(t, orders.placed)
	require.Equal(t, "1.75", result.Progress.Symbols["BTC-USDT"].Effective)
	require.Equal(t, "1.25", result.Progress.Symbols["BTC-USDT"].Residual)
}

func TestConvergeRecoversCompatiblePendingAndUnknownOrders(t *testing.T) {
	tests := []struct {
		name        string
		state       orderdomain.State
		wantSubmit  bool
		wantResolve bool
	}{
		{"pending", orderdomain.Pending, true, false},
		{"submitting", orderdomain.Submitting, false, true},
		{"submit unknown", orderdomain.SubmitUnknown, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, state, orders := newSpotExecutor("2", "1")
			active := activeOrderRecord("recover-me", exchange.SideBuy, "1", "0")
			active.State = string(test.state)
			state.orders = []store.OrderRecord{active}

			result, err := executor.Converge(
				context.Background(),
				"space-1",
				"binding-1",
			)

			require.NoError(t, err)
			require.Equal(t, StatusRunning, result.Status)
			if test.wantSubmit {
				require.Equal(t, []string{"recover-me"}, orders.submitted)
			} else {
				require.Empty(t, orders.submitted)
			}
			if test.wantResolve {
				require.Equal(t, []string{"recover-me"}, orders.resolved)
			} else {
				require.Empty(t, orders.resolved)
			}
			require.Empty(t, orders.placed)
		})
	}
}

func TestConvergeDiscardsConflictingPendingOrder(t *testing.T) {
	executor, state, orders := newSpotExecutor("2", "1")
	active := activeOrderRecord("discard-me", exchange.SideSell, "1", "0")
	active.State = string(orderdomain.Pending)
	state.orders = []store.OrderRecord{active}

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusRunning, result.Status)
	require.Equal(t, []string{"discard-me"}, orders.discarded)
	require.Empty(t, orders.placed)
}

func TestConvergeCancelsOppositeOrderBeforePlacing(t *testing.T) {
	executor, state, orders := newSpotExecutor("3", "1")
	state.orders = []store.OrderRecord{activeOrderRecord(
		"open-sell",
		exchange.SideSell,
		"0.5",
		"0",
	)}

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusRunning, result.Status)
	require.Equal(t, []string{"open-sell"}, orders.canceled)
	require.Empty(t, orders.placed)
}

func TestConvergeCancelsNonReduceOrderBeforeSwapReversal(t *testing.T) {
	executor, state, orders := newSwapExecutor("-2", "1")
	active := activeOrderRecord("unsafe-reversal", exchange.SideSell, "1", "0")
	active.ReduceOnly = false
	state.orders = []store.OrderRecord{active}

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusRunning, result.Status)
	require.Equal(t, []string{"unsafe-reversal"}, orders.canceled)
	require.Empty(t, orders.placed)
}

func TestConvergeExpiredTargetCreatesNoChild(t *testing.T) {
	executor, state, orders := newSpotExecutor("2", "1")
	state.execution.NotAfter = executor.now().Add(-time.Second).UnixMilli()

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	require.Empty(t, orders.placed)
	require.Equal(t, "1", state.execution.ResidualQuantity)
}

func TestConvergeExpiredTargetWinsOverPausedAccountAndDiscardsPending(t *testing.T) {
	executor, state, orders := newSpotExecutor("2", "1")
	state.account.Ready = false
	state.execution.NotAfter = executor.now().Add(-time.Second).UnixMilli()
	active := activeOrderRecord("never-submitted", exchange.SideBuy, "1", "0")
	active.State = string(orderdomain.Pending)
	active.StrategyExecutionID = state.execution.ExecutionID
	state.orders = []store.OrderRecord{active}

	first, err := executor.Converge(context.Background(), "space-1", "binding-1")
	require.NoError(t, err)
	require.Equal(t, StatusRunning, first.Status)
	require.Equal(t, []string{"never-submitted"}, orders.discarded)
	require.Empty(t, orders.submitted)

	state.orders = nil
	second, err := executor.Converge(context.Background(), "space-1", "binding-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, second.Status)
	require.Equal(t, StatusExpired, state.execution.Status)
}

func TestExpiredExecutionDoesNotCancelAnotherBindingsChild(t *testing.T) {
	executor, state, orders := newSpotExecutor("0", "0")
	state.execution.NotAfter = executor.now().Add(-time.Second).UnixMilli()
	active := activeOrderRecord("other-child", exchange.SideBuy, "1", "0")
	active.StrategyExecutionID = "other-execution"
	state.orders = []store.OrderRecord{active}

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	require.Empty(t, orders.canceled)
	require.Empty(t, orders.discarded)
	require.Empty(t, orders.placed)
}

func TestExpiredExecutionWaitsForItsSubmittedChild(t *testing.T) {
	executor, state, orders := newSpotExecutor("0", "0")
	state.execution.NotAfter = executor.now().Add(-time.Second).UnixMilli()
	active := activeOrderRecord("own-child", exchange.SideBuy, "1", "0")
	active.StrategyExecutionID = state.execution.ExecutionID
	state.orders = []store.OrderRecord{active}

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusRunning, result.Status)
	require.Equal(
		t,
		"own-child",
		result.Progress.Symbols["BTC-USDT"].ActiveOrderID,
	)
	require.Empty(t, orders.canceled)
	require.Empty(t, orders.placed)
}

func TestConvergeCompletesWithBelowMinimumResidual(t *testing.T) {
	executor, state, orders := newSpotExecutor("1.05", "1")
	state.instrument.ExchangeQuantityStep = "0.1"
	state.instrument.MinExchangeQuantity = "0.1"

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusCompleted, result.Status)
	require.Empty(t, orders.placed)
	require.Equal(t, "0.05", state.execution.ResidualQuantity)
}

func TestConvergePlacesMarketChildAndCapsNotional(t *testing.T) {
	executor, _, orders := newSpotExecutor("5", "0")
	executor.MaxChildNotional = shared.MustDecimal("200")

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusRunning, result.Status)
	require.Len(t, orders.placed, 1)
	require.Equal(t, exchange.OrderTypeMarket, orders.placed[0].Type)
	require.Equal(t, exchange.FillPolicyUnspecified, orders.placed[0].FillPolicy)
	require.Equal(t, exchange.SideBuy, orders.placed[0].Side)
	require.Equal(t, "2", orders.placed[0].Quantity.String())
	require.False(t, orders.placed[0].ReducePositionOnly)
	require.Equal(t, []string{"child-order"}, orders.submitted)
}

func TestConvergeHoldsBindingLockAcrossChildCreation(t *testing.T) {
	executor, state, orders := newSpotExecutor("1", "0")
	placeStarted := make(chan struct{})
	releasePlace := make(chan struct{})
	orders.placeHook = func() {
		close(placeStarted)
		<-releasePlace
	}
	converged := make(chan error, 1)
	go func() {
		_, err := executor.Converge(context.Background(), "space-1", "binding-1")
		converged <- err
	}()
	<-placeStarted

	lockAcquired := make(chan struct{})
	go func() {
		unlock := state.LockTargetBinding("space-1", "binding-1")
		close(lockAcquired)
		unlock()
	}()
	select {
	case <-lockAcquired:
		t.Fatal("new target could enter while the old target was creating a child")
	case <-time.After(25 * time.Millisecond):
	}

	close(releasePlace)
	require.NoError(t, <-converged)
	select {
	case <-lockAcquired:
	case <-time.After(time.Second):
		t.Fatal("binding lock was not released after convergence")
	}
}

func TestConvergeRejectsChildCapBelowExchangeMinimum(t *testing.T) {
	executor, state, orders := newSpotExecutor("100", "0")
	state.instrument.ExchangeQuantityStep = "0.1"
	state.instrument.MinExchangeQuantity = "0.1"
	state.instrument.MinNotional = "10"
	executor.MaxChildNotional = shared.MustDecimal("5")

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.ErrorIs(t, err, ErrInvalidTarget)
	require.Equal(t, StatusFailed, result.Status)
	require.Equal(t, StatusFailed, state.execution.Status)
	require.Empty(t, orders.placed)
}

func TestConvergeSwapActions(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		target     string
		side       exchange.Side
		quantity   string
		reduceOnly bool
	}{
		{"long grows", "1", "2", exchange.SideBuy, "1", false},
		{"long shrinks", "2", "1", exchange.SideSell, "1", true},
		{"long closes before short", "1", "-2", exchange.SideSell, "1", true},
		{"zero opens short", "0", "-2", exchange.SideSell, "2", false},
		{"short closes before long", "-1", "2", exchange.SideBuy, "1", true},
		{"zero opens long", "0", "2", exchange.SideBuy, "2", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, _, orders := newSwapExecutor(test.target, test.current)

			result, err := executor.Converge(
				context.Background(),
				"space-1",
				"binding-1",
			)

			require.NoError(t, err)
			require.Equal(t, StatusRunning, result.Status)
			require.Len(t, orders.placed, 1)
			spec := orders.placed[0]
			require.Equal(t, test.side, spec.Side)
			require.Equal(t, test.quantity, spec.Quantity.String())
			require.Equal(t, test.reduceOnly, spec.ReducePositionOnly)
			require.Equal(t, exchange.PositionSideNet, spec.PositionSide)
		})
	}
}

func TestConvergeRejectsNegativeSpotTarget(t *testing.T) {
	executor, state, orders := newSpotExecutor("-1", "0")

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.ErrorIs(t, err, ErrInvalidTarget)
	require.Equal(t, StatusFailed, result.Status)
	require.Empty(t, orders.placed)
	require.Equal(t, StatusFailed, state.execution.Status)
}

func TestConvergePausesWhileExchangeAccountIsNotReady(t *testing.T) {
	executor, state, orders := newSpotExecutor("1", "0")
	state.account.Ready = false

	result, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status)
	require.Equal(t, StatusPaused, state.execution.Status)
	require.Contains(t, state.execution.LastError, ErrExecutionPaused.Error())
	require.Empty(t, orders.placed)
}

func TestConvergeReturnsPriceErrorWithoutPlacing(t *testing.T) {
	executor, _, orders := newSpotExecutor("1", "0")
	executor.Prices = priceSourceStub{err: errors.New("price unavailable")}

	_, err := executor.Converge(context.Background(), "space-1", "binding-1")

	require.EqualError(t, err, "price unavailable")
	require.Empty(t, orders.placed)
}

func newSpotExecutor(
	targetQuantity string,
	currentQuantity string,
) (*Executor, *targetStoreStub, *targetOrderServiceStub) {
	now := time.UnixMilli(10_000)
	state := baseTargetState(targetQuantity, now)
	state.account.MarketType = string(exchange.MarketTypeSpot)
	state.account.Snapshot.Balances = []store.AssetBalance{{
		Asset: "BTC", Total: currentQuantity, Available: currentQuantity,
	}}
	state.instrument.MarketType = string(exchange.MarketTypeSpot)
	state.instrument.ContractValue = "0"
	state.instrument.MinExchangeQuantity = "0.001"
	orders := &targetOrderServiceStub{}
	return &Executor{
		Store: state, Orders: orders,
		Prices: priceSourceStub{quote: Quote{
			Price: shared.MustDecimal("100"), UpdatedAt: now,
		}},
		Now: func() time.Time { return now },
	}, state, orders
}

func newSwapExecutor(
	targetQuantity string,
	currentQuantity string,
) (*Executor, *targetStoreStub, *targetOrderServiceStub) {
	executor, state, orders := newSpotExecutor(targetQuantity, "0")
	state.account.MarketType = string(exchange.MarketTypeSwap)
	state.account.SettlementAsset = "USDT"
	state.account.MarginMode = string(exchange.MarginModeCross)
	state.instrument.MarketType = string(exchange.MarketTypeSwap)
	state.instrument.ContractValue = "0.001"
	state.instrument.ContractValueAsset = "BTC"
	state.instrument.ExchangeQuantityStep = "1"
	state.instrument.MinExchangeQuantity = "1"
	state.position = store.PositionRecord{
		SpaceID: "space-1", ExchangeAccountID: "account-1",
		Symbol: "BTC-USDT", PositionSide: string(exchange.PositionSideNet),
		SignedQuantity: currentQuantity,
	}
	state.hasPosition = true
	return executor, state, orders
}

func baseTargetState(targetQuantity string, now time.Time) *targetStoreStub {
	return &targetStoreStub{
		execution: store.TargetExecutionRecord{
			SpaceID: "space-1", ExecutionID: "execution-1", EventID: "execution-1",
			StrategyRunID: "run-1", ExecutionBindingID: "binding-1",
			ExchangeAccountID: "account-1", CommandSequence: 1,
			NotAfter: now.Add(time.Minute).UnixMilli(), DataRevision: "revision-1",
			Targets: []store.TargetPosition{{
				InstrumentID: "BTC-USDT", Symbol: "BTC-USDT",
				TargetQuantity: targetQuantity,
			}},
			Status: StatusRunning,
		},
		account: store.ExchangeAccountRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-1",
			Exchange: string(exchange.ExchangeBinance),
			Status:   "ENABLED", Ready: true,
		},
		instrument: store.InstrumentRecord{
			Exchange: string(exchange.ExchangeBinance),
			Symbol:   "BTC-USDT", InstrumentID: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001",
			PriceTick: "0.1", MinNotional: "0", Status: "TRADING",
		},
	}
}

func activeOrderRecord(
	orderID string,
	side exchange.Side,
	quantity string,
	filled string,
) store.OrderRecord {
	return store.OrderRecord{
		OrderID: orderID, State: string(orderdomain.Open), Side: string(side),
		Quantity: quantity, FilledQuantity: filled, Source: "TARGET",
	}
}
