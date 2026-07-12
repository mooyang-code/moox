package command

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
