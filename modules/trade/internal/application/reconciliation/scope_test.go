package reconciliation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scopeAdapter struct{}

func (scopeAdapter) Place(context.Context, exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "exchange-order", Status: "OPEN"}, nil
}
func (scopeAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}
func (scopeAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "OPEN"}, nil
}
func (scopeAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{BaseAsset: "BTC", QuoteAsset: "USDT"}, nil
}
func (scopeAdapter) ListFills(_ context.Context, symbol, _ string) ([]exchange.FillEvent, error) {
	return []exchange.FillEvent{{
		ExchangeTradeID: "fill-" + symbol, Symbol: symbol, Side: "BUY", BaseAsset: "BTC", QuoteAsset: "USDT",
		Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("10"), Fee: shared.Zero(),
	}}, nil
}
func (scopeAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error { return nil }

func TestScopeFiltersBoundsAndAppliesFillsIdempotently(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{ID: "seed", BizType: "seed", RefType: "test", RefID: "seed", Entries: []ledger.Entry{
			{AccountID: "clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("200").Neg()},
			{AccountID: "acct", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("200")},
		}})
	}))
	engine := &command.Engine{Store: db, Adapter: scopeAdapter{}}
	seedScopeOrder(t, engine, "order-a", "BTC-USDT")
	seedScopeOrder(t, engine, "order-b", "ETH-USDT")
	reconciler := Reconciler{Store: db, Engine: engine}

	first, err := reconciler.Scope(ctx, Scope{SpaceID: "space", AccountID: "acct", Symbol: "BTC-USDT", Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, 1, first.OrdersScanned)
	assert.Equal(t, 1, first.FillsApplied)

	second, err := reconciler.Scope(ctx, Scope{SpaceID: "space", AccountID: "acct", Symbol: "BTC-USDT", Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, 0, second.FillsApplied)
	fills, total, err := db.ListFillsPage(ctx, "space", store.FillQuery{AccountID: "acct", Symbol: "BTC-USDT", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, fills, 1)
}

func seedScopeOrder(t *testing.T, engine *command.Engine, orderID, symbol string) {
	t.Helper()
	placed, err := engine.Place(context.Background(), command.PlaceInput{
		SpaceID: "space", OrderID: orderID, ClientOrderID: orderID + "-client", AccountID: "acct", ChannelID: "channel", Symbol: symbol,
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10",
	})
	require.NoError(t, err)
	_, err = engine.Submit(context.Background(), "space", placed.OrderID, "")
	require.NoError(t, err)
}
