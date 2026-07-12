package command

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cancelStubAdapter struct {
	cancelResult exchange.ExchangeOrderResult
	cancelErr    error
	queryResult  exchange.ExchangeOrderResult
}

func (s cancelStubAdapter) Place(context.Context, exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-1", Status: "OPEN"}, nil
}
func (s cancelStubAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return s.cancelResult, s.cancelErr
}
func (s cancelStubAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return s.queryResult, nil
}
func (s cancelStubAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{BaseAsset: "BTC", QuoteAsset: "USDT"}, nil
}
func (s cancelStubAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (s cancelStubAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

func openOpenOrder(t *testing.T, adapter exchange.TradingAdapter) (*Engine, store.OrderRecord) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	seedUSDTBalance(t, s, "space-1", "acct-1")
	engine := &Engine{Store: s, Adapter: adapter}
	placed, err := engine.Place(context.Background(), PlaceInput{
		SpaceID: "space-1", OrderID: "order-1", ClientOrderID: "client-1",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	submitted, err := engine.Submit(context.Background(), "space-1", placed.OrderID, "")
	require.NoError(t, err)
	require.Equal(t, string(order.Open), submitted.State)
	return engine, submitted
}

func TestEngine_Cancel_OpenOrder_ShouldConfirmCanceled(t *testing.T) {
	engine, rec := openOpenOrder(t, cancelStubAdapter{
		cancelResult: exchange.ExchangeOrderResult{Status: "CANCELED"},
	})
	got, err := engine.Cancel(context.Background(), "space-1", rec.OrderID)
	require.NoError(t, err)
	assert.Equal(t, string(order.Canceled), got.State)
}

func TestEngine_Cancel_TransportUncertain_ShouldMarkCancelUnknown(t *testing.T) {
	engine, rec := openOpenOrder(t, cancelStubAdapter{
		cancelErr: &exchange.ClassifiedError{Category: exchange.ErrorTransportUncertain, Err: errors.New("timeout")},
	})
	got, err := engine.Cancel(context.Background(), "space-1", rec.OrderID)
	assert.Error(t, err)
	assert.Equal(t, string(order.CancelUnknown), got.State)
}

func TestEngine_ReconcileExchangeTerminal_Canceled_ShouldReleaseReservation(t *testing.T) {
	engine, rec := openOpenOrder(t, stubTradingAdapter{})
	got, err := engine.ReconcileExchangeTerminal(context.Background(), "space-1", rec.OrderID, "CANCELED")
	require.NoError(t, err)
	assert.Equal(t, string(order.Canceled), got.State)
}

func TestEngine_ReconcileExchangeCanceled_ShouldDelegateTerminalCanceled(t *testing.T) {
	engine, rec := openOpenOrder(t, stubTradingAdapter{})
	got, err := engine.ReconcileExchangeCanceled(context.Background(), "space-1", rec.OrderID)
	require.NoError(t, err)
	assert.Equal(t, string(order.Canceled), got.State)
}

func TestEngine_RecoverCanceling_QueryOpen_ShouldReturnOpen(t *testing.T) {
	engine, rec := openOpenOrder(t, cancelStubAdapter{
		queryResult: exchange.ExchangeOrderResult{ExchangeOrderID: "ex-1", Status: "OPEN"},
	})
	expected := rec.Version
	rec.State = string(order.Canceling)
	rec.Version++
	require.NoError(t, engine.Store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateOrder(rec, expected)
	}))

	got, err := engine.RecoverCanceling(context.Background(), "space-1", rec.OrderID)

	require.NoError(t, err)
	assert.Equal(t, string(order.Open), got.State)
	assert.Equal(t, "ex-1", got.ExchangeOrderID)
}

func TestEngine_Replace_EmptySagaID_ShouldReturnError(t *testing.T) {
	engine := &Engine{Store: &store.Store{}}
	_, err := engine.Replace(context.Background(), "", "old", PlaceInput{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "saga id required")
}
