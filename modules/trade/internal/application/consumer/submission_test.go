package consumer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubmissionWorker_Handle_NonReadyState_ShouldReturnUnchanged(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	rec := store.OrderRecord{
		SpaceID:        "space-1",
		OrderID:        "order-1",
		ClientOrderID:  "client-1",
		AccountID:      "acct-1",
		ChannelID:      "chan-1",
		Symbol:         "BTC-USDT",
		BaseAsset:      "BTC",
		QuoteAsset:     "USDT",
		Side:           "BUY",
		Quantity:       "1",
		Price:          "100",
		FilledQuantity: "0",
		State:          string(order.Open),
		Version:        1,
	}
	err = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(&rec)
	})
	require.NoError(t, err)

	worker := SubmissionWorker{Engine: &command.Engine{Store: s}}
	got, err := worker.Handle(ctx, "space-1", "order-1")
	require.NoError(t, err)
	assert.Equal(t, string(order.Open), got.State)
}

func TestSubmissionWorker_Handle_ReadyOrder_ShouldSubmit(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space-1", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed"), BizType: "seed", RefType: "test", RefID: "seed",
			Entries: []ledger.Entry{
				{AccountID: "exchange-clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("1000").Neg()},
				{AccountID: "acct-1", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("1000")},
			},
		})
	}))

	engine := &command.Engine{
		Store:   s,
		Adapter: stubSubmitAdapter{},
	}
	placed, err := engine.Place(ctx, command.PlaceInput{
		SpaceID: "space-1", OrderID: "order-2", ClientOrderID: "client-2",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	require.Equal(t, string(order.Ready), placed.State)

	worker := SubmissionWorker{Engine: engine}
	got, err := worker.Handle(ctx, "space-1", "order-2")
	require.NoError(t, err)
	assert.Equal(t, string(order.Open), got.State)
}

type stubSubmitAdapter struct{}

func (stubSubmitAdapter) Place(context.Context, exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-2", Status: "OPEN"}, nil
}
func (stubSubmitAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{}, nil
}
func (stubSubmitAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{}, nil
}
func (stubSubmitAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{}, nil
}
func (stubSubmitAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (stubSubmitAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}
