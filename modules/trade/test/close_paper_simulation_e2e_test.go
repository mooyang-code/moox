package test

import (
	"context"
	"errors"
	"testing"

	papersimulation "github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestClosePaperSimulationCancelsReservationsAndSurvivesRestartE2E(t *testing.T) {
	ctx := context.Background()
	f := newRestingPaperFixture(t, exchange.MarketTypeSpot)
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: "logical-close", Name: "close",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "test setup",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: testSpace, LogicalAccountID: "logical-close",
			TradingAccountID: testAccount, Enabled: true, Priority: 1,
		})
	}))

	spec := marketSpec("close-open-order", exchange.SideBuy, "0.01")
	spec.Type = exchange.OrderTypeLimit
	spec.FillPolicy = exchange.FillPolicyGTC
	limit := shared.MustDecimal("49000")
	spec.LimitPrice = &limit
	placed := mustPlace(t, f, spec)
	open, err := f.orders.Submit(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, open.State)

	require.NoError(t, (&papersimulation.Service{Store: f.store}).Close(ctx, testSpace, testAccount))
	closed, err := f.store.GetOrder(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, "CANCELED", closed.State)
	require.Equal(t, "0", closed.RemainingReservedQuantity)
	account, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	require.Equal(t, "DISABLED", account.Status)
	logical, err := f.store.GetLogicalAccount(ctx, testSpace, "logical-close")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logical.AutomationState)

	f.close(t)
	restarted, err := store.Open(f.path)
	require.NoError(t, err)
	defer restarted.Close()
	candidates, err := restarted.ListPaperMatchCandidates(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, candidates)
}

func TestPaperCancelThroughOrderServiceReleasesReservationE2E(t *testing.T) {
	ctx := context.Background()
	f := newProductionPaperFixture(t, exchange.MarketTypeSpot)
	placed := mustPlace(t, f, marketSpec("paper-cancel", exchange.SideBuy, "0.01"))
	open, err := f.orders.Submit(ctx, testSpace, string(placed.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, open.State)

	canceled, err := f.orders.Cancel(ctx, testSpace, string(open.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Canceled, canceled.State)
	record, err := f.store.GetOrder(ctx, testSpace, string(open.ID))
	require.NoError(t, err)
	require.Equal(t, "CANCELED", record.State)
	require.Equal(t, "0", record.RemainingReservedQuantity)
}

func TestPaperSnapshotFailsClosedWhenValuationQuoteErrorsE2E(t *testing.T) {
	for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
		t.Run(string(market), func(t *testing.T) {
			f := newPaperFixture(t, market)
			spec := marketSpec("valuation-error", exchange.SideBuy, "0.01")
			if market == exchange.MarketTypeSwap {
				spec = swapSpec("valuation-error", exchange.SideBuy, "1", false)
			}
			order := mustPlace(t, f, spec)
			_, err := f.orders.Submit(context.Background(), testSpace, string(order.ID))
			require.NoError(t, err)
			f.fake.mu.Lock()
			f.fake.quoteErr = errors.New("public quote unavailable")
			f.fake.mu.Unlock()
			_, err = f.adapter.GetAccountSnapshot(context.Background())
			require.Error(t, err)
		})
	}
}
