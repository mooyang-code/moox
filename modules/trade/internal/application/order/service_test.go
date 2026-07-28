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

	"github.com/mooyang-code/moox/modules/trade/internal/domain/exchangeaccount"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type adapterSourceStub struct{ adapter *adapterStub }

func (s adapterSourceStub) Adapter(string) (exchange.Adapter, error) {
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
	fills       []exchange.Fill
	fillsErr    error
	placeHook   func()
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
	string,
	string,
) ([]exchange.Fill, string, error) {
	return a.fills, "", a.fillsErr
}
func (a *adapterStub) GetOrder(context.Context, string, string) (exchange.Order, error) {
	return a.getResult, a.getErr
}
func (a *adapterStub) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	a.placeCalls++
	if a.placeHook != nil {
		a.placeHook()
	}
	return a.placeResult, a.placeErr
}
func (a *adapterStub) CancelOrder(context.Context, string, string) (exchange.Order, error) {
	a.cancelCalls++
	return exchange.Order{}, a.cancelErr
}
func (a *adapterStub) SetLeverage(context.Context, string, shared.Decimal) error {
	return nil
}
func (a *adapterStub) SetMarginMode(context.Context, string, exchange.MarginMode) error {
	return nil
}
func (a *adapterStub) SubscribePrivate(context.Context, exchange.EventHandler) error {
	return nil
}

type syncerStub struct{ calls int }

func (s *syncerStub) SyncAccount(context.Context, string) error {
	s.calls++
	return nil
}

func TestServicePlacePersistsBeforeSubmissionAndIsIdempotent(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	now := time.Unix(1_700_000_000, 0)
	spec := testSpec(now)

	got, err := service.Place(context.Background(), spec)
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
	replayed, err := service.Place(context.Background(), spec)
	require.NoError(t, err)
	require.Equal(t, got.ID, replayed.ID)
	require.Equal(t, 1, adapter.placeCalls)

	conflict := spec
	conflict.Quantity = shared.MustDecimal("2")
	_, err = service.Place(context.Background(), conflict)
	require.ErrorIs(t, err, ErrIdempotencyConflict)
}

func TestServiceDiscardPendingReleasesReservationWithoutExchangeCall(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	now := time.Unix(1_700_000_000, 0)
	_, err := service.Place(context.Background(), testSpec(now))
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
	projections, err := tradeStore.ListBalanceProjections(
		context.Background(),
		"space-1",
		"account-1",
	)
	require.NoError(t, err)
	for _, projection := range projections {
		require.True(t, projection.Amount.IsZero(), projection)
	}
}

func TestServicePlaceTransportUnknownRetainsReservation(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}

	pending, err := service.Place(
		context.Background(),
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
	projections, getErr := tradeStore.ListBalanceProjections(
		context.Background(),
		"space-1",
		"account-1",
	)
	require.NoError(t, getErr)
	require.Len(t, projections, 2)
}

func TestServiceSubmitRevalidatesReadinessAndReferencePrice(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	pending, err := service.Place(
		context.Background(),
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	service.Validator.Accounts = accountEligibilityFunc(func(
		context.Context,
		string,
	) (exchangeaccount.Account, error) {
		return exchangeaccount.Account{}, exchangeaccount.ErrAccountNotExecutable
	})
	_, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.ErrorIs(t, err, exchangeaccount.ErrAccountNotExecutable)
	require.Equal(t, 0, adapter.placeCalls)
	stored, err := tradeStore.GetOrder(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, "PENDING", stored.State)

	service, tradeStore, adapter = newTestService(t)
	pending, err = service.Place(
		context.Background(),
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

func TestServiceSubmitAcceptsFillThatRacesWithAcknowledgement(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	pending, err := service.Place(
		context.Background(),
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
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	placed, err = service.Submit(context.Background(), "space-1", string(placed.ID))
	require.NoError(t, err)

	_, err = service.Cancel(context.Background(), "space-1", string(placed.ID))
	require.ErrorIs(t, err, ErrServiceConfig)
	require.Equal(t, 0, adapter.cancelCalls)
}

func TestServiceRejectedSubmissionReleasesReservation(t *testing.T) {
	service, tradeStore, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorRejected, Err: errors.New("bad order")}

	pending, err := service.Place(
		context.Background(),
		testSpec(time.Unix(1_700_000_000, 0)),
	)
	require.NoError(t, err)
	got, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)
	require.Equal(t, "REJECTED", string(got.State))

	projections, getErr := tradeStore.ListBalanceProjections(
		context.Background(),
		"space-1",
		"account-1",
	)
	require.NoError(t, getErr)
	for _, projection := range projections {
		require.True(t, projection.Amount.IsZero())
	}
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
			_, errs[index] = service.Place(context.Background(), specs[index])
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
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	account := executableAccount(exchange.MarketTypeSpot)
	instrument := testInstrument(exchange.MarketTypeSpot)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateExchangeAccount(store.ExchangeAccountRecord{
			SpaceID: account.SpaceID, ExchangeAccountID: account.ID, Name: account.Name,
			Exchange: string(account.Exchange), MarketType: string(account.MarketType),
			ExecutionMode:      string(account.ExecutionMode),
			CredentialSecretID: account.CredentialSecretID,
			SettlementAsset:    account.SettlementAsset, Status: string(account.Status),
			Ready: true,
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: string(instrument.Exchange), MarketType: string(instrument.MarketType),
			Symbol: instrument.Symbol, InstrumentID: "BTCUSDT",
			BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
			SettlementAsset:      instrument.SettlementAsset,
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
			SpaceID:          "space-1",
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
