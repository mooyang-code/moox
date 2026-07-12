package command

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
