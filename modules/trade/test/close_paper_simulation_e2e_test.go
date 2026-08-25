package test

import (
	"context"
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
