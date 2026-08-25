package test

import (
	"context"
	"io"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestSubmitFailuresRemainUnknownAndRecoverByClientIDWithoutReplace(t *testing.T) {
	failures := []struct {
		name string
		err  error
	}{
		{name: "EOF", err: &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: io.EOF}},
		{name: "timeout", err: transportError("request timeout")},
		{name: "429", err: &exchange.Error{Kind: exchange.ErrorRateLimited, HTTPStatus: 429}},
		{name: "5xx", err: &exchange.Error{Kind: exchange.ErrorTransportUnknown, HTTPStatus: 503}},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			ctx := context.Background()
			fake := newFakeExchange(exchange.MarketTypeSpot)
			fake.placeErr = failure.err
			f := newFixture(t, exchange.MarketTypeSpot, fake)
			pending := mustPlace(t, f, marketSpec("unknown-"+failure.name, exchange.SideBuy, "0.01"))

			unknown, err := f.orders.Submit(ctx, testSpace, string(pending.ID))
			require.Error(t, err)
			require.Equal(t, orderdomain.SubmitUnknown, unknown.State)
			require.Equal(t, 1, fake.placeCalls)

			fake.mu.Lock()
			fake.placeErr = nil
			fake.orders[unknown.Spec.ClientOrderID] = exchange.Order{
				ExchangeOrderID: "accepted-despite-response-loss",
				ClientOrderID:   unknown.Spec.ClientOrderID, Symbol: testSymbol,
				OrderType: exchange.OrderTypeMarket, Side: exchange.SideBuy,
				Quantity: unknown.Spec.Quantity, Status: exchange.OrderStatusOpen,
				CreatedAt: testNow, UpdatedAt: testNow,
			}
			fake.mu.Unlock()
			recovered, err := f.orders.ResolveUnknown(ctx, testSpace, string(unknown.ID))
			require.NoError(t, err)
			require.Equal(t, orderdomain.Open, recovered.State)
			require.Equal(t, 1, fake.lookupCalls)
			require.Equal(t, 1, fake.placeCalls, "recovery must query, not place again")
		})
	}
}

func TestCanceledOrderAcceptsLateFillAndDeduplicatesIt(t *testing.T) {
	ctx := context.Background()
	fake := newFakeExchange(exchange.MarketTypeSpot)
	f := newFixture(t, exchange.MarketTypeSpot, fake)
	pending := mustPlace(t, f, marketSpec("cancel-late-fill", exchange.SideBuy, "0.01"))
	open, err := f.orders.Submit(ctx, testSpace, string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, open.State)
	canceled, err := f.orders.Cancel(ctx, testSpace, string(open.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Canceled, canceled.State)
	canceled, err = f.orders.Get(ctx, testSpace, string(open.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Canceled, canceled.State)

	fill := fake.emitFill(open.Spec.ClientOrderID, "late-trade", "0.004", "50000", "0")
	applied, err := f.reducer.ApplyFill(ctx, fill, consumerSource())
	require.NoError(t, err)
	require.True(t, applied)
	applied, err = f.reducer.ApplyFill(ctx, fill, consumerSource())
	require.NoError(t, err)
	require.False(t, applied)

	record, err := f.store.GetOrder(ctx, testSpace, string(open.ID))
	require.NoError(t, err)
	require.Equal(t, string(orderdomain.PartiallyCanceled), record.State)
	require.Equal(t, "0.004", record.FilledQuantity)
	_, total, err := f.store.ListFills(
		ctx,
		testSpace,
		store.FillQuery{TradingAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)

	regressed := fake.orders[open.Spec.ClientOrderID]
	regressed.Status = exchange.OrderStatusPartiallyFilled
	regressed.FilledQuantity = sharedZero()
	err = f.sync.ApplyOrder(ctx, testAccount, regressed)
	require.NoError(t, err)
	after, err := f.store.GetOrder(ctx, testSpace, string(open.ID))
	require.NoError(t, err)
	require.Equal(t, "0.004", after.FilledQuantity, "snapshot regression must not erase persisted Fill facts")
}

func consumerSource() consumer.Source {
	return consumer.Source{
		SpaceID: testSpace, TradingAccountID: testAccount,
		Kind: consumer.OriginPrivateSocket,
	}
}

func sharedZero() shared.Decimal { return shared.Zero() }
