package order

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type adapterSourceStub struct{ adapter *adapterStub }

func (s adapterSourceStub) Adapter(string) (execution.ExecutionAdapter, error) {
	return s.adapter, nil
}

type adapterStub struct {
	placeResult exchange.Order
	placeErr    error
	cancelErr   error
	placeCalls  int
	cancelCalls int
	getResult   exchange.Order
	getErr      error
	getCalls    int
	fills       []exchange.Fill
	fillsErr    error
	placeHook   func()
	placed      exchange.OrderRequest
}

func (a *adapterStub) Exchange() exchange.Exchange { return exchange.ExchangeBinance }
func (a *adapterStub) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	return nil, nil
}
func (a *adapterStub) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{}, nil
}
func (a *adapterStub) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, nil
}
func (a *adapterStub) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return nil, nil
}

func (a *adapterStub) ListRecentFills(
	context.Context,
	shared.ExchangeSymbol,
	string,
) ([]exchange.Fill, string, error) {
	return a.fills, "", a.fillsErr
}
func (a *adapterStub) GetOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	a.getCalls++
	return a.getResult, a.getErr
}
func (a *adapterStub) PlaceOrder(_ context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	a.placeCalls++
	a.placed = request
	if a.placeHook != nil {
		a.placeHook()
	}
	return a.placeResult, a.placeErr
}
func (a *adapterStub) CancelOrder(context.Context, shared.ExchangeSymbol, string) (exchange.Order, error) {
	a.cancelCalls++
	return exchange.Order{}, a.cancelErr
}
func (a *adapterStub) SetLeverage(context.Context, shared.ExchangeSymbol, shared.Decimal) error {
	return nil
}
func (a *adapterStub) SetMarginMode(context.Context, shared.ExchangeSymbol, exchange.MarginMode) error {
	return nil
}
func (a *adapterStub) Subscribe(_ context.Context, handler execution.AccountEventHandler) error {
	handler.OnSubscribed()
	return nil
}

type syncerStub struct {
	calls int
	err   error
}

func (s *syncerStub) SyncAccount(context.Context, string) error {
	s.calls++
	return s.err
}

func TestServicePlacePersistsBeforeSubmissionAndIsIdempotent(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	now := time.Unix(1_700_000_000, 0)
	spec := testSpec(now)

	got, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.Equal(t, "PENDING", string(got.State))
	require.Equal(t, 0, adapter.placeCalls)

	record, err := tradeStore.GetOrder(context.Background(), "space-1", "order-1")
	require.NoError(t, err)
	require.Nil(t, record.LimitPrice)
	require.Equal(t, "101", record.ReservedQuantity)
	require.Equal(t, "101", record.RemainingReservedQuantity)
	require.Empty(t, record.ExchangeOrderID)

	got, err = service.Submit(context.Background(), "space-1", "order-1")
	require.NoError(t, err)
	require.Equal(t, "OPEN", string(got.State))
	require.Equal(t, 1, adapter.placeCalls)
	record, err = tradeStore.GetOrder(context.Background(), "space-1", "order-1")
	require.NoError(t, err)
	require.Equal(t, "exchange-order-1", record.ExchangeOrderID)

	service.Validator.Now = func() time.Time {
		return time.Unix(1_700_000_100, 0)
	}
	replayed, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.Equal(t, got.ID, replayed.ID)
	require.Equal(t, 1, adapter.placeCalls)

	conflict := spec
	conflict.Quantity = shared.MustDecimal("2")
	_, err = service.Place(context.Background(), "space-1", conflict)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
}

func TestSubmitPendingSubmitsOnce(t *testing.T) {
	service, _, adapter := newTestService(t)
	pending, err := service.Place(context.Background(), "space-1", testSpec(service.now()))
	require.NoError(t, err)

	submitted, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, submitted.State)
	require.Equal(t, 1, adapter.placeCalls)

	replayed, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, submitted.State, replayed.State)
	require.Equal(t, 1, adapter.placeCalls)
}

func TestSubmitTargetRejectsExternalFactBeforeExchangeCall(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	spec := testSpec(service.now())
	setTestOwner(&spec, orderdomain.OwnerTarget)
	pending, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "external-order",
			TradingAccountID: "account-1", ClientOrderID: "external-client",
			ExchangeOrderID: "external-exchange-order",
			Symbol:          "BTC-USDT", OrderType: "MARKET", Side: "BUY",
			Quantity: "1", ReferencePrice: "100",
			ReferencePriceAt: service.now().UnixMilli(),
			OwnerType:        "EXTERNAL", OwnerID: "external-exchange-order",
			LogicalAccountID: "logical-1", State: "OPEN", Version: 1,
		})
	}))

	got, err := service.Submit(
		context.Background(), "space-1", string(pending.ID),
	)

	require.ErrorIs(t, err, ErrExternalConflict)
	require.Equal(t, orderdomain.Pending, got.State)
	require.Zero(t, adapter.placeCalls)
}

func TestSubmitTargetRechecksAccountReadinessBeforeExchangeCall(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	spec := testSpec(service.now())
	setTestOwner(&spec, orderdomain.OwnerTarget)
	pending, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountReadiness(
			"space-1", "account-1", false,
			service.now().UnixMilli(), "private facts awaiting sync",
		)
	}))

	got, err := service.Submit(
		context.Background(), "space-1", string(pending.ID),
	)

	require.ErrorIs(t, err, ErrAccountNotReady)
	require.Equal(t, orderdomain.Pending, got.State)
	require.Zero(t, adapter.placeCalls)
}

func TestSubmitSubmittingQueriesExchangeBeforeRetry(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	pending, err := service.Place(context.Background(), "space-1", testSpec(service.now()))
	require.NoError(t, err)
	setStoredOrderState(t, tradeStore, string(pending.ID), orderdomain.Submitting, service.now())
	adapter.getResult = exchange.Order{ExchangeOrderID: "found-1", Status: exchange.OrderStatusOpen}

	got, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, got.State)
	require.Equal(t, 1, adapter.getCalls)
	require.Zero(t, adapter.placeCalls)
}

func TestSubmitUnknownQueriesExchangeBeforeRetry(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	pending, err := service.Place(context.Background(), "space-1", testSpec(service.now()))
	require.NoError(t, err)
	setStoredOrderState(t, tradeStore, string(pending.ID), orderdomain.SubmitUnknown, service.now())
	adapter.getResult = exchange.Order{ExchangeOrderID: "found-1", Status: exchange.OrderStatusOpen}

	got, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, got.State)
	require.Equal(t, 1, adapter.getCalls)
	require.Zero(t, adapter.placeCalls)
}

func TestSubmitUnknownResetsToPendingOnlyAfterConfirmedAbsentAndWindowExpired(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	pending, err := service.Place(context.Background(), "space-1", testSpec(service.now()))
	require.NoError(t, err)
	setStoredOrderState(t, tradeStore, string(pending.ID), orderdomain.SubmitUnknown, service.now())
	adapter.getErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound}
	service.UnknownLookupWindow = time.Minute

	withinWindow, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.SubmitUnknown, withinWindow.State)
	require.Zero(t, adapter.placeCalls)

	service.Now = func() time.Time { return time.Unix(1_700_000_061, 0) }
	service.Validator.Now = service.Now
	expired, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Pending, expired.State)
	require.Zero(t, adapter.placeCalls)

	retried, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, retried.State)
	require.Equal(t, 1, adapter.placeCalls)
	require.Equal(t, "client-1", adapter.placed.ClientOrderID)
}

func TestSubmitOpenPartialOrTerminalReturnsStoredOrder(t *testing.T) {
	for _, state := range []orderdomain.State{
		orderdomain.Open,
		orderdomain.PartiallyFilled,
		orderdomain.Filled,
		orderdomain.Canceled,
		orderdomain.PartiallyCanceled,
		orderdomain.Expired,
	} {
		t.Run(string(state), func(t *testing.T) {
			service, tradeStore, adapter := newTestService(t)
			pending, err := service.Place(context.Background(), "space-1", testSpec(service.now()))
			require.NoError(t, err)
			setStoredOrderState(t, tradeStore, string(pending.ID), state, service.now())

			got, err := service.Submit(context.Background(), "space-1", string(pending.ID))
			require.NoError(t, err)
			require.Equal(t, state, got.State)
			require.Zero(t, adapter.placeCalls)
			require.Zero(t, adapter.getCalls)
		})
	}
}

func TestSubmitRejectedReturnsStableRejection(t *testing.T) {
	service, _, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorRejected, Err: errors.New("stable reason")}
	pending, err := service.Place(context.Background(), "space-1", testSpec(service.now()))
	require.NoError(t, err)
	_, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.EqualError(t, err, "REJECTED: stable reason")

	got, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.EqualError(t, err, "REJECTED: stable reason")
	require.Equal(t, orderdomain.Rejected, got.State)
	require.Equal(t, 1, adapter.placeCalls)
}

func TestSubmitSameClientFieldsIgnoresServerReferencePriceAndTime(t *testing.T) {
	service, _, _ := newTestService(t)
	spec := testSpec(service.now())
	first, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)

	spec.ReferencePrice = shared.MustDecimal("101")
	spec.ReferencePriceAt = service.now().Add(500 * time.Millisecond)
	replayed, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.Equal(t, first.ID, replayed.ID)
}

func TestSubmitSameClientIDWithDifferentClientFieldsConflicts(t *testing.T) {
	service, _, _ := newTestService(t)
	spec := testSpec(service.now())
	_, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)

	spec.Side = exchange.SideSell
	_, err = service.Place(context.Background(), "space-1", spec)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
}

func TestPlaceGeneratesAndPersistsClientOrderIDOnce(t *testing.T) {
	service, _, adapter := newTestService(t)
	spec := testSpec(service.now())
	spec.ClientOrderID = ""

	placed, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.Len(t, placed.Spec.ClientOrderID, 20)
	require.Regexp(t, `^[a-z0-9]+$`, placed.Spec.ClientOrderID)

	submitted, err := service.Submit(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, placed.Spec.ClientOrderID, submitted.Spec.ClientOrderID)
	require.Equal(t, 1, adapter.placeCalls)
}

func TestOrderServiceDerivesReducePositionOnlyForSwapReduction(t *testing.T) {
	service, _, _ := newTestServiceForMarket(t, exchange.MarketTypeSwap)
	service.Validator.Positions = positionSourceStub{
		position: exchange.Position{SignedQuantity: shared.MustDecimal("2")},
	}
	spec := testSpec(service.now())
	spec.PositionSide = exchange.PositionSideNet
	spec.Side = exchange.SideSell

	placed, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.True(t, placed.Spec.ReducePositionOnly)
}

func TestOrderServiceLeavesReducePositionOnlyFalseForSwapIncrease(t *testing.T) {
	service, _, _ := newTestServiceForMarket(t, exchange.MarketTypeSwap)
	service.Validator.Positions = positionSourceStub{
		position: exchange.Position{SignedQuantity: shared.MustDecimal("2")},
	}
	spec := testSpec(service.now())
	spec.PositionSide = exchange.PositionSideNet
	spec.Side = exchange.SideBuy
	spec.ReducePositionOnly = true

	placed, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.False(t, placed.Spec.ReducePositionOnly)
}

func TestOrderServiceRejectsManualOrderThatWouldCrossZero(t *testing.T) {
	service, _, _ := newTestServiceForMarket(t, exchange.MarketTypeSwap)
	service.Validator.Positions = positionSourceStub{
		position: exchange.Position{SignedQuantity: shared.MustDecimal("2")},
	}
	spec := testSpec(service.now())
	spec.PositionSide = exchange.PositionSideNet
	spec.Side = exchange.SideSell
	spec.Quantity = shared.MustDecimal("3")
	setTestOwner(&spec, orderdomain.OwnerOperator)

	_, err := service.Place(context.Background(), "space-1", spec)
	require.ErrorIs(t, err, ErrCrossZero)
}

func TestOrderServiceRejectsTargetOrderThatWouldCrossZero(t *testing.T) {
	service, _, _ := newTestServiceForMarket(t, exchange.MarketTypeSwap)
	service.Validator.Positions = positionSourceStub{
		position: exchange.Position{SignedQuantity: shared.MustDecimal("2")},
	}
	spec := testSpec(service.now())
	spec.PositionSide = exchange.PositionSideNet
	spec.Side = exchange.SideSell
	spec.Quantity = shared.MustDecimal("3")
	setTestOwner(&spec, orderdomain.OwnerTarget)

	_, err := service.Place(context.Background(), "space-1", spec)
	require.ErrorIs(t, err, ErrCrossZero)
}

func TestOrderServiceOperatorFlattenCannotCrossZero(t *testing.T) {
	service, _, _ := newTestServiceForMarket(t, exchange.MarketTypeSwap)
	service.Validator.Positions = positionSourceStub{
		position: exchange.Position{SignedQuantity: shared.MustDecimal("2")},
	}
	spec := testSpec(service.now())
	spec.PositionSide = exchange.PositionSideNet
	spec.Side = exchange.SideSell
	spec.Quantity = shared.MustDecimal("3")
	setTestOwner(&spec, orderdomain.OwnerOperator)

	_, err := service.Place(context.Background(), "space-1", spec)
	require.ErrorIs(t, err, ErrCrossZero)
}

func TestSubmitRejectsPositionChangeThatWouldCrossZero(t *testing.T) {
	for _, owner := range []orderdomain.OwnerType{
		orderdomain.OwnerTarget,
		orderdomain.OwnerOperator,
	} {
		t.Run(string(owner), func(t *testing.T) {
			service, tradeStore, adapter := newTestServiceForMarket(t, exchange.MarketTypeSwap)
			positions := &positionSourceStub{}
			service.Validator.Positions = positions
			spec := testSpec(service.now())
			spec.PositionSide = exchange.PositionSideNet
			spec.Side = exchange.SideBuy
			setTestOwner(&spec, owner)
			pending, err := service.Place(context.Background(), "space-1", spec)
			require.NoError(t, err)
			require.False(t, pending.Spec.ReducePositionOnly)

			positions.position.SignedQuantity = shared.MustDecimal("-0.5")
			rejected, err := service.Submit(
				context.Background(),
				"space-1",
				string(pending.ID),
			)

			require.ErrorIs(t, err, ErrCrossZero)
			require.Equal(t, orderdomain.Rejected, rejected.State)
			require.Zero(t, adapter.placeCalls)
			stored, getErr := tradeStore.GetOrder(
				context.Background(),
				"space-1",
				string(pending.ID),
			)
			require.NoError(t, getErr)
			require.Equal(t, "REJECTED", stored.State)
			require.False(t, stored.ReduceOnly)
		})
	}
}

func TestSubmitPersistsFreshReducePositionOnlyBeforeSending(t *testing.T) {
	service, tradeStore, adapter := newTestServiceForMarket(t, exchange.MarketTypeSwap)
	positions := &positionSourceStub{}
	service.Validator.Positions = positions
	spec := testSpec(service.now())
	spec.PositionSide = exchange.PositionSideNet
	spec.Side = exchange.SideBuy
	setTestOwner(&spec, orderdomain.OwnerOperator)
	pending, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.False(t, pending.Spec.ReducePositionOnly)

	positions.position.SignedQuantity = shared.MustDecimal("-2")
	submitted, err := service.Submit(
		context.Background(),
		"space-1",
		string(pending.ID),
	)

	require.NoError(t, err)
	require.True(t, adapter.placed.ReduceOnly)
	require.True(t, submitted.Spec.ReducePositionOnly)
	stored, err := tradeStore.GetOrder(
		context.Background(),
		"space-1",
		string(pending.ID),
	)
	require.NoError(t, err)
	require.True(t, stored.ReduceOnly)
}

func TestSubmitRejectsPreviouslyReducingOrderAfterPositionReverses(t *testing.T) {
	for _, owner := range []orderdomain.OwnerType{
		orderdomain.OwnerTarget,
		orderdomain.OwnerOperator,
	} {
		t.Run(string(owner), func(t *testing.T) {
			service, _, adapter := newTestServiceForMarket(t, exchange.MarketTypeSwap)
			positions := &positionSourceStub{
				position: exchange.Position{SignedQuantity: shared.MustDecimal("1")},
			}
			service.Validator.Positions = positions
			spec := testSpec(service.now())
			spec.PositionSide = exchange.PositionSideNet
			spec.Side = exchange.SideSell
			setTestOwner(&spec, owner)
			pending, err := service.Place(context.Background(), "space-1", spec)
			require.NoError(t, err)
			require.True(t, pending.Spec.ReducePositionOnly)

			positions.position.SignedQuantity = shared.MustDecimal("-1")
			rejected, err := service.Submit(
				context.Background(),
				"space-1",
				string(pending.ID),
			)

			require.ErrorIs(t, err, ErrReduceOnly)
			require.Equal(t, orderdomain.Rejected, rejected.State)
			require.Zero(t, adapter.placeCalls)
		})
	}
}

func TestSubmitRejectsOperatorCloseAfterPositionShrinksBelowOrder(t *testing.T) {
	service, _, adapter := newTestServiceForMarket(t, exchange.MarketTypeSwap)
	positions := &positionSourceStub{
		position: exchange.Position{SignedQuantity: shared.MustDecimal("2")},
	}
	service.Validator.Positions = positions
	spec := testSpec(service.now())
	spec.PositionSide = exchange.PositionSideNet
	spec.Side = exchange.SideSell
	spec.Quantity = shared.MustDecimal("2")
	setTestOwner(&spec, orderdomain.OwnerOperator)
	pending, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)

	positions.position.SignedQuantity = shared.MustDecimal("0.5")
	rejected, err := service.Submit(
		context.Background(), "space-1", string(pending.ID),
	)
	require.ErrorIs(t, err, ErrCrossZero)
	require.Equal(t, orderdomain.Rejected, rejected.State)
	require.Zero(t, adapter.placeCalls)
}

func TestConfirmedAbsentRetryRefreshesReducePositionOnly(t *testing.T) {
	service, tradeStore, adapter := newTestServiceForMarket(t, exchange.MarketTypeSwap)
	positions := &positionSourceStub{}
	service.Validator.Positions = positions
	now := time.Unix(1_700_000_000, 0)
	service.Now = func() time.Time { return now }
	service.Validator.Now = func() time.Time { return now }
	spec := testSpec(now)
	spec.PositionSide = exchange.PositionSideNet
	spec.Side = exchange.SideBuy
	setTestOwner(&spec, orderdomain.OwnerOperator)
	pending, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)

	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	unknown, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)
	require.Equal(t, orderdomain.SubmitUnknown, unknown.State)
	require.False(t, adapter.placed.ReduceOnly)

	adapter.getErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound}
	adapter.placeErr = nil
	now = now.Add(31 * time.Second)
	pending, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Pending, pending.State)
	require.Equal(t, 1, adapter.placeCalls)

	positions.position.SignedQuantity = shared.MustDecimal("-2")
	submitted, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, 2, adapter.placeCalls)
	require.True(t, adapter.placed.ReduceOnly)
	require.True(t, submitted.Spec.ReducePositionOnly)
	require.Equal(t, pending.Spec.ClientOrderID, adapter.placed.ClientOrderID)
	stored, getErr := tradeStore.GetOrder(
		context.Background(),
		"space-1",
		string(pending.ID),
	)
	require.NoError(t, getErr)
	require.True(t, stored.ReduceOnly)
}

func TestOrderServiceNeverSetsReducePositionOnlyForSpot(t *testing.T) {
	service, _, _ := newTestServiceForMarket(t, exchange.MarketTypeSpot)
	spec := testSpec(service.now())
	spec.ReducePositionOnly = true

	placed, err := service.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.False(t, placed.Spec.ReducePositionOnly)
}

func TestSubmitNonOpenSuccessSyncsWithoutSettlingAggregateResponse(t *testing.T) {
	service, _, adapter := newTestService(t)
	syncer := &syncerStub{}
	service.Syncer = syncer
	adapter.placeResult = exchange.Order{
		ExchangeOrderID: "exchange-order-1",
		Status:          exchange.OrderStatusFilled,
		FilledQuantity:  shared.MustDecimal("1"),
		AveragePrice:    shared.MustDecimal("100"),
		UpdatedAt:       service.now(),
	}
	pending, err := service.Place(context.Background(), "space-1", testSpec(service.now()))
	require.NoError(t, err)

	got, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, 1, syncer.calls)
	require.Equal(t, orderdomain.Open, got.State)
	require.True(t, got.FilledQuantity.IsZero())
}

func TestServiceDiscardPendingReleasesReservationWithoutExchangeCall(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	now := time.Unix(1_700_000_000, 0)
	_, err := service.Place(context.Background(), "space-1", testSpec(now))
	require.NoError(t, err)

	discarded, err := service.DiscardPending(
		context.Background(),
		"space-1",
		"order-1",
	)

	require.NoError(t, err)
	require.Equal(t, orderdomain.Canceled, discarded.State)
	require.Zero(t, adapter.placeCalls)
	require.Zero(t, adapter.cancelCalls)
	record, err := tradeStore.GetOrder(context.Background(), "space-1", "order-1")
	require.NoError(t, err)
	require.Equal(t, "CANCELED", record.State)
	require.Equal(t, "0", record.RemainingReservedQuantity)
}

func TestServicePlaceUncertainResultRetainsReservation(t *testing.T) {
	for _, kind := range []exchange.ErrorKind{
		exchange.ErrorTransportUnknown,
		exchange.ErrorRateLimited,
	} {
		t.Run(string(kind), func(t *testing.T) {
			service, tradeStore, adapter := newTestService(t)
			adapter.placeErr = &exchange.Error{Kind: kind}

			pending, err := service.Place(
				context.Background(),
				"space-1",
				testSpec(time.Unix(1_700_000_000, 0)),
			)
			require.NoError(t, err)
			got, err := service.Submit(context.Background(), "space-1", string(pending.ID))
			require.Error(t, err)
			require.Equal(t, "SUBMIT_UNKNOWN", string(got.State))

			record, getErr := tradeStore.GetOrder(context.Background(), "space-1", "order-1")
			require.NoError(t, getErr)
			require.Equal(t, "SUBMIT_UNKNOWN", record.State)
			require.Equal(t, "101", record.RemainingReservedQuantity)
		})
	}
}

func TestServiceSubmitRevalidatesReadinessAndReferencePrice(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	pending, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	service.Validator.Accounts = accountEligibilityFunc(func(
		context.Context,
		string,
	) (tradingaccount.Account, error) {
		return tradingaccount.Account{}, tradingaccount.ErrAccountNotExecutable
	})
	_, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.ErrorIs(t, err, tradingaccount.ErrAccountNotExecutable)
	require.Equal(t, 0, adapter.placeCalls)
	stored, err := tradeStore.GetOrder(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "PENDING", stored.State)

	service, tradeStore, adapter = newTestService(t)
	pending, err = service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	service.Validator.Now = func() time.Time {
		return time.Unix(1_700_000_002, 0)
	}
	_, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.ErrorIs(t, err, orderdomain.ErrInvalidSpec)
	require.Equal(t, 0, adapter.placeCalls)
	stored, err = tradeStore.GetOrder(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "REJECTED", stored.State)
	require.Equal(t, "0", stored.RemainingReservedQuantity)
	require.Positive(t, stored.FinishedAt)
}

func TestServiceResolveUnknownFindsOrderOrReturnsPendingAfterWindow(t *testing.T) {
	service, _, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	pending, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	unknown, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)
	require.Equal(t, "SUBMIT_UNKNOWN", string(unknown.State))

	adapter.getErr = nil
	adapter.getResult = exchange.Order{ExchangeOrderID: "recovered-order"}
	resolved, err := service.ResolveUnknown(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "OPEN", string(resolved.State))
	require.Equal(t, "recovered-order", resolved.ExchangeOrderID)

	service, _, adapter = newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	pending, err = service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	_, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)
	stored, err := service.Store.GetOrder(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, int64(1_700_000_000_000), stored.SubmittedAt)
	adapter.getErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound}
	service.UnknownLookupWindow = time.Minute

	adapter.fills = []exchange.Fill{
		{ClientOrderID: "client-1", ExchangeOrderID: "exchange-a"},
		{ClientOrderID: "client-1", ExchangeOrderID: "exchange-b"},
	}
	resolved, err = service.ResolveUnknown(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "SUBMIT_UNKNOWN", string(resolved.State))
	adapter.fills = nil
	service.Now = func() time.Time { return time.Unix(1_700_000_061, 0) }
	resolved, err = service.ResolveUnknown(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "PENDING", string(resolved.State))
}

func TestServiceResolveUnknownFoundSynchronizesAuthoritativeState(t *testing.T) {
	service, _, adapter := newTestService(t)
	syncer := &syncerStub{}
	service.Syncer = syncer
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	pending, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	_, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)

	adapter.getErr = nil
	adapter.getResult = exchange.Order{
		ExchangeOrderID: "recovered-order",
		Status:          exchange.OrderStatusFilled,
		FilledQuantity:  shared.MustDecimal("1"),
		UpdatedAt:       service.now(),
	}
	resolved, err := service.ResolveUnknown(
		context.Background(),
		"space-1",
		string(pending.ID),
	)

	require.NoError(t, err)
	require.Equal(t, 1, syncer.calls)
	require.Equal(t, orderdomain.Open, resolved.State)
	require.Equal(t, "recovered-order", resolved.ExchangeOrderID)
}

func TestServiceSubmitUnknownFoundSynchronizesAuthoritativeState(t *testing.T) {
	service, _, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	pending, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	_, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)

	syncer := &syncerStub{}
	service.Syncer = syncer
	adapter.getErr = nil
	adapter.getResult = exchange.Order{
		ExchangeOrderID: "recovered-order",
		Status:          exchange.OrderStatusFilled,
		FilledQuantity:  shared.MustDecimal("1"),
		UpdatedAt:       service.now(),
	}
	resolved, err := service.Submit(
		context.Background(),
		"space-1",
		string(pending.ID),
	)

	require.NoError(t, err)
	require.Equal(t, 1, syncer.calls)
	require.Equal(t, orderdomain.Open, resolved.State)
	require.Equal(t, "recovered-order", resolved.ExchangeOrderID)
}

func TestServiceSubmitAcceptsFillThatRacesWithAcknowledgement(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	pending, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	adapter.placeHook = func() {
		record, hookErr := tradeStore.GetOrder(
			context.Background(),
			"space-1",
			string(pending.ID),
		)
		require.NoError(t, hookErr)
		expected := record.Version
		record.ExchangeOrderID = "exchange-order-1"
		record.FilledQuantity = record.Quantity
		record.AveragePrice = "100"
		record.RemainingReservedQuantity = "0"
		record.State = "FILLED"
		record.Version++
		require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
			return tx.UpdateOrder(record, expected)
		}))
	}

	got, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "FILLED", string(got.State))
	require.Equal(t, "exchange-order-1", got.ExchangeOrderID)
}

func TestServiceResolveUnknownRecoversSubmittingAfterCrash(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	pending, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	record, err := tradeStore.GetOrder(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	expected := record.Version
	record.State = "SUBMITTING"
	record.SubmittedAt = 1_700_000_000_000
	record.Version++
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}))
	adapter.getErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound}
	service.UnknownLookupWindow = time.Minute

	got, err := service.ResolveUnknown(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "SUBMIT_UNKNOWN", string(got.State))
}

func TestServiceCancelWaitsForAccountSyncBeforeTerminalRelease(t *testing.T) {
	service, tradeStore, _ := newTestService(t)
	syncer := &syncerStub{}
	service.Syncer = syncer
	placed, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	placed, err = service.Submit(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, err)

	got, err := service.Cancel(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, "CANCELING", string(got.State))
	require.Equal(t, 1, syncer.calls)

	record, err := tradeStore.GetOrder(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, "CANCELING", record.State)
	require.Equal(t, "101", record.RemainingReservedQuantity)
}

func TestServiceCancelRejectionRestoresOpenState(t *testing.T) {
	service, _, adapter := newTestService(t)
	service.Syncer = &syncerStub{}
	placed, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	placed, err = service.Submit(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, err)
	adapter.cancelErr = &exchange.Error{Kind: exchange.ErrorRejected}

	got, err := service.Cancel(context.Background(), "space-1", string(placed.ID))
	require.Error(t, err)
	require.Equal(t, "OPEN", string(got.State))
}

func TestServiceCancelRequiresAccountSyncBeforeDispatch(t *testing.T) {
	service, _, adapter := newTestService(t)
	placed, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	placed, err = service.Submit(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, err)

	_, err = service.Cancel(context.Background(), "space-1", string(placed.ID))
	require.ErrorIs(t, err, ErrServiceConfig)
	require.Equal(t, 0, adapter.cancelCalls)
}

func TestServiceRecoverCancelRetriesCancelingAndUnknown(t *testing.T) {
	service, _, adapter := newTestService(t)
	syncer := &syncerStub{err: errors.New("sync unavailable")}
	service.Syncer = syncer
	placed, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	placed, err = service.Submit(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, err)

	_, err = service.Cancel(context.Background(), "space-1", string(placed.ID))
	require.EqualError(t, err, "sync unavailable")
	syncer.err = nil
	recovered, err := service.RecoverCancel(
		context.Background(),
		"space-1",
		string(placed.ID),
	)
	require.NoError(t, err)
	require.Equal(t, orderdomain.Canceling, recovered.State)
	require.Equal(t, 2, adapter.cancelCalls)

	adapter.cancelErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	_, err = service.RecoverCancel(context.Background(), "space-1", string(placed.ID))
	require.Error(t, err)
	stored, getErr := service.Get(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, getErr)
	require.Equal(t, orderdomain.CancelUnknown, stored.State)

	adapter.cancelErr = nil
	_, err = service.RecoverCancel(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, err)
}

func TestServiceRejectedSubmissionReleasesReservation(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorRejected, Err: errors.New("bad order")}

	pending, err := service.Place(
		context.Background(),
		"space-1",
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	got, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)
	require.Equal(t, "REJECTED", string(got.State))

	record, getErr := tradeStore.GetOrder(context.Background(), "space-1", "order-1")
	require.NoError(t, getErr)
	require.Equal(t, "0", record.RemainingReservedQuantity)
	require.Equal(t, int64(1_700_000_000_000), record.FinishedAt)
}

func TestServiceConcurrentPlaceCannotOverReserveSnapshot(t *testing.T) {
	service, _, _ := newTestService(t)
	service.Validator.MaxChildNotional = shared.MustDecimal("10000")
	var sequence atomic.Int64
	service.NewOrderID = func() string {
		return "order-" + fmt.Sprint(sequence.Add(1))
	}
	specs := []orderdomain.OrderSpec{
		testSpec(time.Unix(1_700_000_000, 0)),
		testSpec(time.Unix(1_700_000_000, 0)),
	}
	specs[0].ClientOrderID = "client-a"
	specs[1].ClientOrderID = "client-b"
	for i := range specs {
		specs[i].Quantity = shared.MustDecimal("6")
	}

	errs := make([]error, len(specs))
	var wait sync.WaitGroup
	for i := range specs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errs[index] = service.Place(context.Background(), "space-1", specs[index])
		}(i)
	}
	wait.Wait()

	successes := 0
	insufficient := 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrInsufficientFunds) {
			insufficient++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, insufficient)
}

func newTestService(t *testing.T) (*Service, *store.Store, *adapterStub) {
	return newTestServiceForMarket(t, exchange.MarketTypeSpot)
}

func newTestServiceForMarket(
	t *testing.T,
	market exchange.MarketType,
) (*Service, *store.Store, *adapterStub) {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	account := executableAccount(market)
	instrument := testInstrument(market)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: account.SpaceID, TradingAccountID: account.ID, Name: account.Name,
			Exchange: string(account.Exchange), MarketType: string(account.MarketType),
			ExecutionMode:      string(account.ExecutionMode),
			Environment:        string(account.Environment),
			CredentialSecretID: account.CredentialSecretID,
			SettlementAsset:    account.SettlementAsset, Status: string(account.Status),
			Ready: true,
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: account.SpaceID, LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", ExecutionMode: string(account.ExecutionMode),
			MarketType: string(account.MarketType), SettlementAsset: account.SettlementAsset,
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: account.SpaceID, LogicalAccountID: "logical-1",
			TradingAccountID: account.ID, Enabled: true,
		}); err != nil {
			return err
		}
		if err := tx.SetLogicalAccountAutomation(
			account.SpaceID, "logical-1", "ACTIVE", "",
		); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: string(instrument.Exchange), MarketType: string(instrument.MarketType),
			Symbol: instrument.Symbol, InstrumentID: "BTCUSDT",
			BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
			SettlementAsset:      instrument.SettlementAsset,
			Linear:               instrument.Linear,
			ContractValue:        instrument.ContractValue.String(),
			ContractValueAsset:   instrument.ContractValueAsset,
			ExchangeQuantityStep: instrument.ExchangeQuantityStep.String(),
			MinExchangeQuantity:  instrument.MinExchangeQuantity.String(),
			PriceTick:            instrument.PriceTick.String(), MinNotional: instrument.MinNotional.String(),
			Status: instrument.Status,
		})
	}))
	adapter := &adapterStub{
		placeResult: exchange.Order{ExchangeOrderID: "exchange-order-1"},
	}
	service := &Service{
		Store: tradeStore,
		Validator: Validator{
			Accounts:         accountEligibilityStub{account: account},
			Instruments:      instrumentSourceStub{instrument: instrument},
			Positions:        positionSourceStub{},
			Now:              func() time.Time { return time.Unix(1_700_000_000, 0) },
			MaxReferenceAge:  time.Second,
			MaxChildNotional: shared.MustDecimal("1000"),
			FeeBufferRate:    shared.MustDecimal("0.01"),
		},
		Adapters:   adapterSourceStub{adapter: adapter},
		NewOrderID: func() string { return "order-1" },
		Now:        func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	return service, tradeStore, adapter
}

func setStoredOrderState(
	t *testing.T,
	tradeStore *store.Store,
	orderID string,
	state orderdomain.State,
	submittedAt time.Time,
) {
	t.Helper()
	record, err := tradeStore.GetOrder(context.Background(), "space-1", orderID)
	require.NoError(t, err)
	expected := record.Version
	record.State = string(state)
	record.SubmittedAt = submittedAt.UnixMilli()
	record.Version++
	if state == orderdomain.Rejected {
		record.RejectReason = "stable reason"
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	}))
}
