package test

import (
	"context"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// The cursor must include the exact full-close Fill even when multiple orders
// share one submission timestamp.
func TestPaperSwapFullCloseSyncPersistsClosingFill(t *testing.T) {
	ctx := context.Background()
	f := newPaperFixture(t, exchange.MarketTypeSwap)

	open := mustPlace(t, f, swapSpec("paper-open", exchange.SideBuy, "0.01", false))
	open, err := f.orders.Submit(ctx, testSpace, string(open.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, open.State)

	closeSpec := swapSpec("paper-close", exchange.SideSell, "0.01", true)
	closeSpec.ReferencePrice = shared.MustDecimal("51000")
	closeOrder := mustPlace(t, f, closeSpec)
	closeOrder, err = f.orders.Submit(ctx, testSpace, string(closeOrder.ID))
	require.NoError(t, err)

	_, total, err := f.store.ListFills(
		ctx,
		testSpace,
		store.FillQuery{TradingAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	if closeOrder.State != orderdomain.Filled || total != 2 {
		t.Fatalf(
			"Paper full-close sync: state=%s fills=%d, want FILLED and 2; closing Fill was skipped",
			closeOrder.State,
			total,
		)
	}
}

func TestPaperSwapCursorSurvivesClockRollback(t *testing.T) {
	ctx := context.Background()
	f := newPaperFixture(t, exchange.MarketTypeSwap)

	open := mustPlace(t, f, swapSpec("rollback-open", exchange.SideBuy, "0.01", false))
	_, err := f.orders.Submit(ctx, testSpace, string(open.ID))
	require.NoError(t, err)

	rolledBack := testNow.Add(-time.Minute)
	f.orders.Now = func() time.Time { return rolledBack }
	f.orders.Validator.Now = func() time.Time { return rolledBack }
	f.fake.mu.Lock()
	f.fake.reference.UpdatedAt = rolledBack
	f.fake.mu.Unlock()
	closeSpec := swapSpec("rollback-close", exchange.SideSell, "0.01", true)
	closeSpec.ReferencePrice = shared.MustDecimal("51000")
	closeSpec.ReferencePriceAt = rolledBack
	closeOrder := mustPlace(t, f, closeSpec)
	closeOrder, err = f.orders.Submit(ctx, testSpace, string(closeOrder.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Filled, closeOrder.State)

	_, total, err := f.store.ListFills(
		ctx,
		testSpace,
		store.FillQuery{TradingAccountID: testAccount, Limit: 10},
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
}
