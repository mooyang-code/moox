package logicalaccount

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestAddMemberRequiresAdoptionForExistingExposure(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-b",
			Symbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS",
			ExchangeUpdatedAt: 1_900,
		})
	}))

	err := service.AddMember(context.Background(), AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		ExchangeAccountID: "account-b", Enabled: true, Priority: 2,
	})
	require.ErrorIs(t, err, ErrAdoptionRequired)

	require.NoError(t, service.AddMember(context.Background(), AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		ExchangeAccountID: "account-b", Enabled: true, Priority: 2,
		AdoptExistingExposure: true,
	}))
}

func TestRemoveMemberRejectsActiveOrdersOrPositions(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-a",
			Symbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS",
			ExchangeUpdatedAt: 1_900,
		})
	}))

	err := service.RemoveMember(
		context.Background(), "space-1", "logical-1", "account-a",
	)
	require.ErrorIs(t, err, ErrMemberHasExposure)
}

func TestDisableMemberCannotAdoptAwayExistingExposure(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", ExchangeAccountID: "account-a",
			Symbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS",
			ExchangeUpdatedAt: 1_900,
		})
	}))

	err := service.AddMember(context.Background(), AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		ExchangeAccountID: "account-a", Enabled: false, Priority: 1,
		AdoptExistingExposure: true,
	})

	require.ErrorIs(t, err, ErrMemberHasExposure)
	_, member, findErr := tradeStore.FindLogicalAccountByExchangeAccount(
		context.Background(), "space-1", "account-a",
	)
	require.NoError(t, findErr)
	require.True(t, member.Enabled)
}

func TestLogicalAccountOwnerRunnerIsExclusive(t *testing.T) {
	service, _ := logicalAccountServiceFixture(t)

	account, err := service.ClaimOwner(
		context.Background(), "space-1", "logical-1", "runner-1",
	)
	require.NoError(t, err)
	require.Equal(t, "runner-1", account.OwnerRunnerID)

	_, err = service.ClaimOwner(
		context.Background(), "space-1", "logical-1", "runner-other",
	)
	require.ErrorIs(t, err, ErrOwnerConflict)

	require.NoError(t, service.ReleaseOwner(
		context.Background(), "space-1", "logical-1", "runner-1",
	))
	account, err = service.ClaimOwner(
		context.Background(), "space-1", "logical-1", "runner-other",
	)
	require.NoError(t, err)
	require.Equal(t, "runner-other", account.OwnerRunnerID)
}

func TestLogicalAccountOwnerRunnerIsExclusiveUnderConcurrency(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2", Name: "logical-2",
			ExecutionMode: "PAPER", MarketType: "SWAP", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	require.NoError(t, service.ReleaseOwner(
		context.Background(), "space-1", "logical-1", "runner-1",
	))

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, logicalAccountID := range []string{"logical-1", "logical-2"} {
		wait.Add(1)
		go func(id string) {
			defer wait.Done()
			_, err := service.ClaimOwner(
				context.Background(), "space-1", id, "runner-shared",
			)
			results <- err
		}(logicalAccountID)
	}
	wait.Wait()
	close(results)
	successes := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrOwnerConflict):
			conflicts++
		default:
			t.Fatalf("ClaimOwner() error = %v", err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
}

func TestClaimOwnerClearsPreviousRunnerSequence(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	_, accepted, err := tradeStore.AcceptLogicalAccountTarget(
		context.Background(),
		store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TargetID: "old-target", RunnerID: "runner-1",
			CommandSequence: 100, Status: "PENDING", AcceptedAt: 2_000,
			Targets: []store.InstrumentTarget{{
				InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
			}},
		},
	)
	require.NoError(t, err)
	require.True(t, accepted)
	require.NoError(t, service.ReleaseOwner(
		context.Background(), "space-1", "logical-1", "runner-1",
	))
	_, err = service.ClaimOwner(
		context.Background(), "space-1", "logical-1", "runner-2",
	)
	require.NoError(t, err)

	_, err = tradeStore.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.Error(t, err)
	_, accepted, err = tradeStore.AcceptLogicalAccountTarget(
		context.Background(),
		store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TargetID: "new-target", RunnerID: "runner-2",
			CommandSequence: 1, Status: "PENDING", AcceptedAt: 2_001,
			Targets: []store.InstrumentTarget{{
				InstrumentID: "BTC-USDT-SWAP", Quantity: "2",
			}},
		},
	)
	require.NoError(t, err)
	require.True(t, accepted)
}

func TestClaimOwnerWaitsForPreviousRunnerTargetOrdersToStop(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	_, accepted, err := tradeStore.AcceptLogicalAccountTarget(
		context.Background(),
		store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TargetID: "old-target", RunnerID: "runner-1",
			CommandSequence: 1, Status: "PENDING", AcceptedAt: 2_000,
			Targets: []store.InstrumentTarget{{
				InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
			}},
		},
	)
	require.NoError(t, err)
	require.True(t, accepted)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "old-target-order",
			ExchangeAccountID: "account-a", ClientOrderID: "old-target-order",
			Symbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
			PositionSide: "NET", Quantity: "1", ReferencePrice: "100",
			ReferencePriceAt: 2_000,
			OwnerType:        "TARGET", OwnerID: "old-target",
			LogicalAccountID: "logical-1", RunnerID: "runner-1",
			State: "OPEN", Version: 1,
		})
	}))
	require.NoError(t, service.ReleaseOwner(
		context.Background(), "space-1", "logical-1", "runner-1",
	))

	_, err = service.ClaimOwner(
		context.Background(), "space-1", "logical-1", "runner-2",
	)
	require.ErrorIs(t, err, ErrNotReady)
	current, getErr := tradeStore.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, getErr)
	require.Equal(t, "old-target", current.TargetID)

	order, err := tradeStore.GetOrder(
		context.Background(), "space-1", "old-target-order",
	)
	require.NoError(t, err)
	expectedVersion := order.Version
	order.State = "CANCELED"
	order.Version++
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateOrder(order, expectedVersion)
	}))
	claimed, err := service.ClaimOwner(
		context.Background(), "space-1", "logical-1", "runner-2",
	)
	require.NoError(t, err)
	require.Equal(t, "runner-2", claimed.OwnerRunnerID)
	_, err = tradeStore.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.Error(t, err)
}

func TestLogicalReadinessRequiresEveryEnabledMemberAndTargetMetadata(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, service.AddMember(context.Background(), AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		ExchangeAccountID: "account-b", Enabled: true, Priority: 2,
	}))
	_, _, err := tradeStore.AcceptLogicalAccountTarget(
		context.Background(),
		store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TargetID: "target-1", RunnerID: "runner-1",
			CommandSequence: 1, Status: "PENDING", AcceptedAt: 2_000,
			Targets: []store.InstrumentTarget{{
				InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
			}},
		},
	)
	require.NoError(t, err)

	readiness, err := service.Readiness(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.True(t, readiness.Ready)

	setLogicalFixtureReady(t, tradeStore, "account-b", false)
	readiness, err = service.Readiness(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.False(t, readiness.Ready)
	require.Contains(t, readiness.Reasons[0], "account-b")
}

func TestLogicalReadinessRejectsTargetForDifferentSettlementAsset(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SWAP", Symbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDC", Linear: true, ContractValue: "0.001",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	_, _, err := tradeStore.AcceptLogicalAccountTarget(
		context.Background(),
		store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TargetID: "target-usdc", RunnerID: "runner-1",
			CommandSequence: 1, Status: "PENDING", AcceptedAt: 2_000,
			Targets: []store.InstrumentTarget{{
				InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
			}},
		},
	)
	require.NoError(t, err)

	readiness, err := service.Readiness(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.False(t, readiness.Ready)
	require.Contains(t, readiness.Reasons, "target instrument BTC-USDT-SWAP is unavailable")
}

func TestResumeRequiresReadyNoConflictAndWarnsAboutReopen(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	_, _, err := tradeStore.AcceptLogicalAccountTarget(
		context.Background(),
		store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TargetID: "target-1", RunnerID: "runner-1",
			CommandSequence: 1, Status: "PENDING", AcceptedAt: 2_000,
			Targets: []store.InstrumentTarget{{
				InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
			}},
		},
	)
	require.NoError(t, err)

	account, warning, err := service.Resume(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
	require.Contains(t, warning, "重新开仓")

	account, err = service.Pause(
		context.Background(), "space-1", "logical-1", "manual intervention",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
	require.Equal(t, "manual intervention", account.PauseReason)

	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		_, _, err := tx.EnsureOperatorAction(store.OperatorActionRecord{
			SpaceID: "space-1", ActionID: "action-1",
			LogicalAccountID: "logical-1", ActionType: "FLATTEN",
			Reason: "flatten", RequestJSON: `{}`, Status: "RUNNING",
		})
		return err
	}))
	_, _, err = service.Resume(
		context.Background(), "space-1", "logical-1",
	)
	require.ErrorIs(t, err, ErrNotReady)
}

func logicalAccountServiceFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	now := time.UnixMilli(2_000).UTC()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, account := range []store.ExchangeAccountRecord{
			logicalFixtureAccount("account-a"),
			logicalFixtureAccount("account-b"),
		} {
			if err := tx.CreateExchangeAccount(account); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", ExecutionMode: "PAPER",
			MarketType: "SWAP", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			ExchangeAccountID: "account-a", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SWAP", Symbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", Linear: true, ContractValue: "0.001",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	return &Service{
		Store: tradeStore, Syncer: noopAccountSyncer{},
		Now:            func() time.Time { return now },
		MaxSnapshotAge: time.Minute,
	}, tradeStore
}

type noopAccountSyncer struct{}

func (noopAccountSyncer) SyncAccount(context.Context, string) error { return nil }

func logicalFixtureAccount(id string) store.ExchangeAccountRecord {
	return store.ExchangeAccountRecord{
		SpaceID: "space-1", ExchangeAccountID: id, Name: id,
		Exchange: "BINANCE", MarketType: "SWAP",
		ExecutionMode: "PAPER", Environment: "PAPER",
		SettlementAsset: "USDT", MarginMode: "CROSS",
		Status: "ENABLED", Ready: true,
		LeverageSettings: store.LeverageSettings{"BTCUSDT": "5"},
		Snapshot: store.ExchangeAccountSnapshot{
			AvailableFunds: "1000",
			Balances: []store.AssetBalance{{
				Asset: "USDT", Available: "1000", Total: "1000",
			}},
		},
		SnapshotSourceTime: 1_900, LastSyncAt: 1_900, LastReadyAt: 1_900,
	}
}

func setLogicalFixtureReady(
	t *testing.T,
	tradeStore *store.Store,
	accountID string,
	ready bool,
) {
	t.Helper()
	account, err := tradeStore.GetExchangeAccountByID(context.Background(), accountID)
	require.NoError(t, err)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateExchangeAccountSync(
			account.SpaceID, account.ExchangeAccountID,
			store.ExchangeAccountSyncState{
				Ready: ready, LeverageSettings: account.LeverageSettings,
				Snapshot:           account.Snapshot,
				SnapshotSourceTime: account.SnapshotSourceTime,
				LastSyncAt:         1_900, LastReadyAt: 1_900,
			},
		)
	}))
}
