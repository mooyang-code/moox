package command

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEngine_Replace_Success_ShouldCreateReplacement(t *testing.T) {
	adapter := cancelStubAdapter{cancelResult: exchange.ExchangeOrderResult{Status: "CANCELED"}}
	engine, old := openOpenOrder(t, adapter)
	sagaID := "saga-1"
	rec, err := engine.Replace(context.Background(), sagaID, old.OrderID, PlaceInput{
		SpaceID: "space-1", OrderID: "order-2", ClientOrderID: "client-2",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "101",
	})
	require.NoError(t, err)
	assert.Equal(t, sagaID, rec.SagaID)
	assert.Equal(t, "order-2", rec.ReplacementOrderID)
	assert.NotEmpty(t, rec.State)
}

func TestEngine_AdvanceReplace_ReplacementReady_ShouldNoop(t *testing.T) {
	adapter := cancelStubAdapter{cancelResult: exchange.ExchangeOrderResult{Status: "CANCELED"}}
	engine, old := openOpenOrder(t, adapter)
	sagaID := "saga-adv"
	rec, err := engine.Replace(context.Background(), sagaID, old.OrderID, PlaceInput{
		SpaceID: "space-1", OrderID: "order-adv", ClientOrderID: "client-adv",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "101",
	})
	require.NoError(t, err)
	got, err := engine.AdvanceReplace(context.Background(), "space-1", sagaID)
	require.NoError(t, err)
	assert.Equal(t, rec.State, got.State)
}

func TestEngine_ResolveCancelUnknown_CanceledOnExchange_ShouldUpdate(t *testing.T) {
	engine, rec := openOpenOrder(t, cancelStubAdapter{
		cancelErr:   &exchange.ClassifiedError{Category: exchange.ErrorTransportUncertain, Err: assert.AnError},
		queryResult: exchange.ExchangeOrderResult{Status: "CANCELED", ExchangeOrderID: "ex-9"},
	})
	_, err := engine.Cancel(context.Background(), "space-1", rec.OrderID)
	assert.Error(t, err)
	got, err := engine.ResolveCancelUnknown(context.Background(), "space-1", rec.OrderID)
	require.NoError(t, err)
	assert.Equal(t, "ex-9", got.ExchangeOrderID)
}

func TestEngine_ReconcileExchangeTerminal_Rejected_ShouldRelease(t *testing.T) {
	engine, rec := openOpenOrder(t, stubTradingAdapter{})
	got, err := engine.ReconcileExchangeTerminal(context.Background(), "space-1", rec.OrderID, "REJECTED")
	require.NoError(t, err)
	assert.Equal(t, string(order.Rejected), got.State)
}

func TestEngine_ReconcileExchangeTerminal_UnsupportedStatus_ShouldError(t *testing.T) {
	engine, rec := openOpenOrder(t, stubTradingAdapter{})
	_, err := engine.ReconcileExchangeTerminal(context.Background(), "space-1", rec.OrderID, "UNKNOWN")
	assert.Error(t, err)
}

func TestEngine_ReplaceSaga_PersistedInStore_ShouldRoundTrip(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	seedUSDTBalance(t, s, "space-1", "acct-1")
	engine := &Engine{Store: s, Adapter: stubTradingAdapter{}}
	placed, err := engine.Place(context.Background(), PlaceInput{
		SpaceID: "space-1", OrderID: "o-old", ClientOrderID: "c-old",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	_, err = engine.Submit(context.Background(), "space-1", placed.OrderID, "")
	require.NoError(t, err)
	_, err = engine.Replace(context.Background(), "saga-store", placed.OrderID, PlaceInput{
		SpaceID: "space-1", OrderID: "o-new", ClientOrderID: "c-new",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "99",
	})
	require.NoError(t, err)
	got, err := s.GetSaga(context.Background(), "space-1", "saga-store")
	require.NoError(t, err)
	assert.Equal(t, "o-new", got.ReplacementOrderID)
}
