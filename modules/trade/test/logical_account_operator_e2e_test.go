package test

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

type logicalAccountE2EPriceSource struct {
	at time.Time
}

func (s logicalAccountE2EPriceSource) LatestPrice(
	context.Context,
	string,
	string,
) (targetapp.Quote, error) {
	return targetapp.Quote{
		Price: shared.MustDecimal("50000"), UpdatedAt: s.at,
	}, nil
}

func TestManualFlattenThenResumeCanReopenLatestTarget(t *testing.T) {
	ctx := context.Background()
	now := testNow
	f := newPaperFixture(t, exchange.MarketTypeSwap)
	seedSwapLogicalAccount(t, f.store)
	delivery := targetDelivery(t, now, "target-reopen", 1, "0.002")
	handled := eventconsumer.HandleTarget(ctx, delivery, eventconsumer.TargetOptions{
		Store: f.store,
		Now:   func() time.Time { return now },
	})
	require.Equal(t, jetstream.ACK, handled.Decision, handled.Err)

	prices := logicalAccountE2EPriceSource{at: now}
	executor := &targetapp.Executor{
		Store: f.store, Orders: f.orders, Prices: prices,
		Now:              func() time.Time { return now },
		MaxChildNotional: shared.MustDecimal("1000000"),
	}
	converged, err := executor.Converge(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, "place", converged.Action)
	for attempts := 0; attempts < 10 && converged.Status != targetapp.StatusConverged; attempts++ {
		converged, err = executor.Converge(ctx, testSpace, testLogicalAccount)
		require.NoError(t, err)
	}
	require.Equal(t, targetapp.StatusConverged, converged.Status)

	operatorService := &operator.Service{
		Store: f.store, Orders: f.orders,
		Syncer: syncBridge{service: f.sync}, Prices: prices,
		Now:                func() time.Time { return now },
		FlattenMaxAttempts: 2, FlattenRetryInterval: time.Millisecond,
	}
	flattened, err := operatorService.FlattenLogicalAccount(ctx, operator.FlattenCommand{
		SpaceID: testSpace, ActionID: "flatten-reopen",
		LogicalAccountID: testLogicalAccount, Reason: "manual risk reset",
	})
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", flattened.Action.Status)
	require.Empty(t, flattened.Accounts[0].Remaining)
	target, err := f.store.GetLogicalAccountTarget(
		ctx, testSpace, testLogicalAccount,
	)
	require.NoError(t, err)
	require.Equal(t, "target-reopen", target.TargetID)

	logicalService := &logicalaccount.Service{
		Store: f.store, Syncer: syncBridge{service: f.sync},
		Now:            func() time.Time { return now },
		MaxSnapshotAge: time.Minute,
	}
	resumed, _, err := logicalService.Resume(
		ctx, testSpace, testLogicalAccount,
	)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", resumed.AutomationState)

	reopened, err := executor.Converge(ctx, testSpace, testLogicalAccount)
	require.NoError(t, err)
	require.Equal(t, "place", reopened.Action)
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	require.NotEmpty(t, f.fake.requests)
	last := f.fake.requests[len(f.fake.requests)-1]
	require.Equal(t, exchange.SideBuy, last.Side)
	require.Equal(t, "0.002", last.Quantity.String())
}

func seedSwapLogicalAccount(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount,
			Name: "E2E swap logical account", OwnerRunnerID: testRunner,
			ExecutionMode: "PAPER", MarketType: "SWAP", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: testSpace, LogicalAccountID: testLogicalAccount,
			TradingAccountID: testAccount, Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation(
			testSpace, testLogicalAccount, "ACTIVE", "",
		)
	}))
}
