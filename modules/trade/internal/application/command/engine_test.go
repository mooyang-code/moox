package command

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestReleaseReservation_ZeroRemaining_ShouldReturnNil(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	rec := store.OrderRecord{
		SpaceID:          "space-1",
		OrderID:          "order-1",
		AccountID:        "acct-1",
		ReservedAsset:    "USDT",
		ReservedAmount:   "100",
		ConsumedReserved: "100",
	}
	err = s.Transaction(context.Background(), func(tx *store.Tx) error {
		return ReleaseReservation(tx, rec)
	})
	assert.NoError(t, err)
}

func TestReleaseReservation_WithRemaining_ShouldPostLedger(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	err = s.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.PostLedger("space-1", ledger.Transaction{
			ID:      shared.LedgerTransactionID("seed:deposit"),
			BizType: "seed",
			RefType: "test",
			RefID:   "deposit",
			Entries: []ledger.Entry{
				{AccountID: "exchange-clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("100").Neg()},
				{AccountID: "acct-1", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("100")},
			},
		}); err != nil {
			return err
		}
		return tx.PostLedger("space-1", ledger.Transaction{
			ID:      shared.LedgerTransactionID("seed:freeze"),
			BizType: "seed",
			RefType: "test",
			RefID:   "freeze",
			Entries: []ledger.Entry{
				{AccountID: "acct-1", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("60").Neg()},
				{AccountID: "acct-1", Asset: "USDT", Bucket: "frozen", Amount: shared.MustDecimal("60")},
			},
		})
	})
	require.NoError(t, err)

	rec := store.OrderRecord{
		SpaceID:          "space-1",
		OrderID:          "order-1",
		AccountID:        "acct-1",
		ReservedAsset:    "USDT",
		ReservedAmount:   "100",
		ConsumedReserved: "40",
	}
	err = s.Transaction(ctx, func(tx *store.Tx) error {
		return ReleaseReservation(tx, rec)
	})
	assert.NoError(t, err)
}

func TestReleaseReservation_ConsumedExceedsReserved_ShouldReturnError(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	rec := store.OrderRecord{
		SpaceID:          "space-1",
		OrderID:          "order-1",
		AccountID:        "acct-1",
		ReservedAsset:    "USDT",
		ReservedAmount:   "100",
		ConsumedReserved: "150",
	}
	err = s.Transaction(context.Background(), func(tx *store.Tx) error {
		return ReleaseReservation(tx, rec)
	})
	assert.Error(t, err)
}

func TestEngine_AdapterFor_NoResolverNoAdapter_ShouldReturnError(t *testing.T) {
	engine := &Engine{Store: &store.Store{}}
	_, err := engine.AdapterFor(context.Background(), store.OrderRecord{SpaceID: "s", ChannelID: "c"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exchange adapter unavailable")
}

type uniqueExchangeIDAdapter struct {
	cancelStubAdapter
	placeCount int
}

func (a *uniqueExchangeIDAdapter) Place(_ context.Context, r exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	a.placeCount++
	return exchange.ExchangeOrderResult{
		ExchangeOrderID: "ex-new-" + r.ClientOrderID,
		ClientOrderID:   r.ClientOrderID,
		Status:          "OPEN",
	}, nil
}

func TestEngine_AdvanceReplace_OpenReplacement_ShouldCompleteSaga(t *testing.T) {
	adapter := &uniqueExchangeIDAdapter{cancelStubAdapter: cancelStubAdapter{
		cancelResult: exchange.ExchangeOrderResult{Status: "CANCELED"},
	}}
	engine, old := openOpenOrder(t, adapter)
	sagaID := "saga-open"
	rec, err := engine.Replace(context.Background(), sagaID, old.OrderID, PlaceInput{
		SpaceID: "space-1", OrderID: "order-new", ClientOrderID: "client-new",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "102",
	})
	require.NoError(t, err)
	_, err = engine.Submit(context.Background(), "space-1", rec.ReplacementOrderID, "")
	require.NoError(t, err)
	got, err := engine.AdvanceReplace(context.Background(), "space-1", sagaID)
	require.NoError(t, err)
	assert.Equal(t, string(execution.SagaReplacementSubmitted), got.State)
}

type stubTradingAdapter struct {
	placeResult exchange.ExchangeOrderResult
	placeErr    error
	queryResult exchange.ExchangeOrderResult
	queryErr    error
}

func (s stubTradingAdapter) Place(context.Context, exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return s.placeResult, s.placeErr
}
func (s stubTradingAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{}, nil
}
func (s stubTradingAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return s.queryResult, s.queryErr
}
func (s stubTradingAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{BaseAsset: "BTC", QuoteAsset: "USDT"}, nil
}
func (s stubTradingAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (s stubTradingAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

func seedUSDTBalance(t *testing.T, s *store.Store, space, account string) {
	t.Helper()
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.PostLedger(space, ledger.Transaction{
			ID:      shared.LedgerTransactionID("seed:deposit"),
			BizType: "seed",
			RefType: "test",
			RefID:   "deposit",
			Entries: []ledger.Entry{
				{AccountID: "exchange-clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("1000").Neg()},
				{AccountID: account, Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("1000")},
			},
		})
	}))
}

func TestEngine_Place_ValidSpotBuy_ShouldCreateReadyOrder(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()
	seedUSDTBalance(t, s, "space-1", "acct-1")

	engine := &Engine{Store: s}
	got, err := engine.Place(context.Background(), PlaceInput{
		SpaceID: "space-1", OrderID: "order-1", ClientOrderID: "client-1",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	assert.Equal(t, string(order.Ready), got.State)
	assert.Equal(t, "USDT", got.ReservedAsset)
}

func TestEngine_Place_IdempotentClientOrderID_ShouldReturnExisting(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()
	seedUSDTBalance(t, s, "space-1", "acct-1")

	engine := &Engine{Store: s}
	in := PlaceInput{
		SpaceID: "space-1", OrderID: "order-1", ClientOrderID: "client-1",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	}
	first, err := engine.Place(context.Background(), in)
	require.NoError(t, err)
	second, err := engine.Place(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, first.OrderID, second.OrderID)
}

func TestEngine_Submit_WithStubAdapter_ShouldAcknowledge(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()
	seedUSDTBalance(t, s, "space-1", "acct-1")

	engine := &Engine{
		Store: s,
		Adapter: stubTradingAdapter{
			placeResult: exchange.ExchangeOrderResult{ExchangeOrderID: "ex-1", Status: "OPEN"},
		},
	}
	placed, err := engine.Place(context.Background(), PlaceInput{
		SpaceID: "space-1", OrderID: "order-1", ClientOrderID: "client-1",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	assert.Equal(t, string(order.Ready), placed.State)

	submitted, err := engine.Submit(context.Background(), "space-1", "order-1", "")
	require.NoError(t, err)
	assert.Equal(t, string(order.Open), submitted.State)
	assert.Equal(t, "ex-1", submitted.ExchangeOrderID)
}

func TestEngine_SubmitPaperNeverCallsExchange(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()
	seedUSDTBalance(t, s, "space-1", "acct-1")

	adapter := &uniqueExchangeIDAdapter{}
	filled := false
	engine := &Engine{
		Store: s, Adapter: adapter,
		ApplyPaperFill: func(_ context.Context, current store.OrderRecord) error {
			filled = true
			assert.Equal(t, "paper", current.ExecutionMode)
			assert.Equal(t, "paper-order-1", current.ExchangeOrderID)
			return nil
		},
	}
	_, err = engine.Place(context.Background(), PlaceInput{
		SpaceID: "space-1", OrderID: "order-1", ClientOrderID: "client-1",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT", ExecutionMode: "paper",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	submitted, err := engine.Submit(context.Background(), "space-1", "order-1", "")
	require.NoError(t, err)
	assert.Equal(t, string(order.Open), submitted.State)
	assert.True(t, filled)
	assert.Zero(t, adapter.placeCount)
}

func TestEngine_RecoverSubmittingPaperNeverCallsExchange(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()
	seedUSDTBalance(t, s, "space-1", "acct-1")

	adapter := &uniqueExchangeIDAdapter{}
	filled := false
	engine := &Engine{
		Store: s, Adapter: adapter,
		ApplyPaperFill: func(context.Context, store.OrderRecord) error {
			filled = true
			return nil
		},
	}
	placed, err := engine.Place(context.Background(), PlaceInput{
		SpaceID: "space-1", OrderID: "order-recover", ClientOrderID: "client-recover",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT", ExecutionMode: "paper",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	placed.State = string(order.Submitting)
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateOrder(placed, placed.Version)
	}))

	recovered, err := engine.RecoverSubmitting(context.Background(), "space-1", placed.OrderID)
	require.NoError(t, err)
	assert.Equal(t, string(order.Open), recovered.State)
	assert.True(t, filled)
	assert.Zero(t, adapter.placeCount)
}

func TestEngine_Submit_RejectedByAdapter_ShouldRejectOrder(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()
	seedUSDTBalance(t, s, "space-1", "acct-1")

	engine := &Engine{
		Store: s,
		Adapter: stubTradingAdapter{
			placeErr: &exchange.ClassifiedError{Category: exchange.ErrorRejected, Err: errors.New("rejected")},
		},
	}
	_, err = engine.Place(context.Background(), PlaceInput{
		SpaceID: "space-1", OrderID: "order-2", ClientOrderID: "client-2",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)

	got, err := engine.Submit(context.Background(), "space-1", "order-2", "")
	require.NoError(t, err)
	assert.Equal(t, string(order.Rejected), got.State)
}

func placeSubmitUnknown(t *testing.T) (*Engine, store.OrderRecord) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	seedUSDTBalance(t, s, "space-1", "acct-1")
	engine := &Engine{
		Store: s,
		Adapter: stubTradingAdapter{
			placeErr: &exchange.ClassifiedError{Category: exchange.ErrorTransportUncertain, Err: errors.New("timeout")},
		},
	}
	placed, err := engine.Place(context.Background(), PlaceInput{
		SpaceID: "space-1", OrderID: "order-u1", ClientOrderID: "client-u1",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	submitted, err := engine.Submit(context.Background(), "space-1", placed.OrderID, "")
	require.NoError(t, err)
	require.Equal(t, string(order.SubmitUnknown), submitted.State)
	return engine, submitted
}

func TestEngine_ResolveUnknown_OrderNotFound_ShouldRetrySubmit(t *testing.T) {
	engine, rec := placeSubmitUnknown(t)
	engine.Adapter = stubTradingAdapter{
		queryErr:    &exchange.ClassifiedError{Category: exchange.ErrorOrderNotFound, Err: errors.New("not found")},
		placeResult: exchange.ExchangeOrderResult{ExchangeOrderID: "ex-2", Status: "OPEN"},
	}
	got, err := engine.ResolveUnknown(context.Background(), "space-1", rec.OrderID)
	require.NoError(t, err)
	assert.Equal(t, string(order.Open), got.State)
}

func TestEngine_ResolveUnknown_OpenOnExchange_ShouldAcknowledge(t *testing.T) {
	engine, rec := placeSubmitUnknown(t)
	engine.Adapter = stubTradingAdapter{
		queryResult: exchange.ExchangeOrderResult{ExchangeOrderID: "ex-1", Status: "OPEN"},
	}
	got, err := engine.ResolveUnknown(context.Background(), "space-1", rec.OrderID)
	require.NoError(t, err)
	assert.Equal(t, string(order.Open), got.State)
	assert.Equal(t, "ex-1", got.ExchangeOrderID)
}

func TestEngine_ResolveUnknown_NotSubmitUnknown_ShouldNoop(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	engine := &Engine{Store: s}
	seedUSDTBalance(t, s, "space-1", "acct-1")
	placed, err := engine.Place(context.Background(), PlaceInput{
		SpaceID: "space-1", OrderID: "order-r1", ClientOrderID: "client-r1",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	got, err := engine.ResolveUnknown(context.Background(), "space-1", placed.OrderID)
	require.NoError(t, err)
	assert.Equal(t, string(order.Ready), got.State)
}

func TestEngine_RecoverSubmitting_ShouldResolveUnknown(t *testing.T) {
	engine, rec := placeSubmitUnknown(t)
	require.NoError(t, engine.Store.Transaction(context.Background(), func(tx *store.Tx) error {
		rec.State = string(order.Submitting)
		return tx.UpdateOrder(rec, rec.Version)
	}))
	engine.Adapter = stubTradingAdapter{
		queryResult: exchange.ExchangeOrderResult{ExchangeOrderID: "ex-9", Status: "OPEN"},
	}
	got, err := engine.RecoverSubmitting(context.Background(), "space-1", rec.OrderID)
	require.NoError(t, err)
	assert.Equal(t, string(order.Open), got.State)
}
