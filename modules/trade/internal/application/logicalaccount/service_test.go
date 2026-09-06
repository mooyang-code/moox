package logicalaccount

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAddMemberRequiresAdoptionForExistingExposure(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-b",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS",
			ExchangeUpdatedAt: 1_900,
		})
	}))

	err := service.AddMember(context.Background(), AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TradingAccountID: "account-b", Enabled: true, Priority: 2,
	})
	require.ErrorIs(t, err, ErrAdoptionRequired)

	require.NoError(t, service.AddMember(context.Background(), AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TradingAccountID: "account-b", Enabled: true, Priority: 2,
		AdoptExistingExposure: true,
	}))
}

func TestRemoveMemberRejectsActiveOrdersOrPositions(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-a",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
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
			SpaceID: "space-1", TradingAccountID: "account-a",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS",
			ExchangeUpdatedAt: 1_900,
		})
	}))

	err := service.AddMember(context.Background(), AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TradingAccountID: "account-a", Enabled: false, Priority: 1,
		AdoptExistingExposure: true,
	})

	require.ErrorIs(t, err, ErrMemberHasExposure)
	_, member, findErr := tradeStore.FindLogicalAccountByTradingAccount(
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

func TestLogicalAccountOwnerCASAcrossStoreConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trade.db")
	first, err := store.Open(path)
	require.NoError(t, err)
	second, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, first.Close())
		require.NoError(t, second.Close())
	})
	require.NoError(t, first.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-cas", Name: "logical-cas",
			ExecutionMode: "PAPER", MarketType: "SWAP", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	services := []*Service{
		{Store: first, Syncer: noopAccountSyncer{}},
		{Store: second, Syncer: noopAccountSyncer{}},
	}
	var wait sync.WaitGroup
	results := make(chan error, len(services))
	for i, service := range services {
		wait.Add(1)
		go func(i int, service *Service) {
			defer wait.Done()
			_, claimErr := service.ClaimOwner(context.Background(), "space-1", "logical-cas", fmt.Sprintf("runner-%d", i))
			results <- claimErr
		}(i, service)
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for claimErr := range results {
		switch {
		case claimErr == nil:
			successes++
		case errors.Is(claimErr, ErrOwnerConflict):
			conflicts++
		default:
			t.Fatalf("cross-connection ClaimOwner() error = %v", claimErr)
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

func TestClaimOwnerClearsReleasedTargetWhenRunnerIsReused(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	_, accepted, err := tradeStore.AcceptLogicalAccountTarget(
		context.Background(),
		store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TargetID: "old-target", RunnerID: "runner-1",
			CommandSequence: 1, Status: "PENDING", AcceptedAt: 2_000,
			Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}},
		},
	)
	require.NoError(t, err)
	require.True(t, accepted)
	require.NoError(t, service.ReleaseOwner(context.Background(), "space-1", "logical-1", "runner-1"))
	_, err = service.ClaimOwner(context.Background(), "space-1", "logical-1", "runner-1")
	require.NoError(t, err)
	_, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.Error(t, err)
}

func TestRebindOwnerFencesAndClearsTargetWithoutPausing(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", ""); err != nil {
			return err
		}
		return tx.SetLogicalAccountOwnerGeneration("space-1", "logical-1", "runner-1")
	}); err != nil {
		t.Fatal(err)
	}
	before, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	_, accepted, err := tradeStore.AcceptLogicalAccountTarget(
		context.Background(),
		store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TargetID: "old-target", RunnerID: "runner-1",
			CommandSequence: 1, Status: "PENDING", AcceptedAt: 2_000,
			Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}},
		},
	)
	require.NoError(t, err)
	require.True(t, accepted)
	claimed, err := service.RebindOwner(context.Background(), "space-1", "logical-1", "runner-1", "archive-key")
	require.NoError(t, err)
	require.Equal(t, "runner-1", claimed.OwnerRunnerID)
	require.Equal(t, "ACTIVE", claimed.AutomationState)
	require.Greater(t, claimed.OwnerGeneration, before.OwnerGeneration)
	_, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.Error(t, err)
}

func TestRebindOwnerClaimsWhenReleaseAlreadyWon(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	claimed, err := service.RebindOwner(context.Background(), "space-1", "logical-1", "runner-1", "archive-key")
	require.NoError(t, err)
	require.Equal(t, "runner-1", claimed.OwnerRunnerID)
	current, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), current.OwnerGeneration)
}

func TestRebindOwnerRetryDoesNotDeleteNewTarget(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountOwnerGeneration("space-1", "logical-1", "runner-1")
	}))
	_, err := service.RebindOwner(context.Background(), "space-1", "logical-1", "runner-1", "archive-key")
	require.NoError(t, err)
	_, accepted, err := tradeStore.AcceptLogicalAccountTarget(context.Background(), store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "new-target", RunnerID: "runner-1",
		CommandSequence: 1, Status: "PENDING", AcceptedAt: 2_001,
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "2"}},
	})
	require.NoError(t, err)
	require.True(t, accepted)
	_, err = service.RebindOwner(context.Background(), "space-1", "logical-1", "runner-1", "archive-key")
	require.NoError(t, err)
	target, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "new-target", target.TargetID)
}

func TestRebindOwnerWaitsForOpenTargetOrders(t *testing.T) {
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountOwnerGeneration("space-1", "logical-1", "runner-1")
	}))
	_, accepted, err := tradeStore.AcceptLogicalAccountTarget(context.Background(), store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "old-target", RunnerID: "runner-1",
		CommandSequence: 1, Status: "PENDING", AcceptedAt: 2_000,
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}},
	})
	require.NoError(t, err)
	require.True(t, accepted)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "old-target-order", TradingAccountID: "account-a", ClientOrderID: "old-target-order",
			ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", PositionSide: "NET", Quantity: "1", ReferencePrice: "100",
			ReferencePriceAt: 2_000, OwnerType: "TARGET", OwnerID: "old-target", LogicalAccountID: "logical-1", RunnerID: "runner-1",
			State: "OPEN", Version: 1,
		})
	}))
	_, err = service.RebindOwner(context.Background(), "space-1", "logical-1", "runner-1", "archive-key")
	require.ErrorIs(t, err, ErrNotReady)
	target, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "old-target", target.TargetID)
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
			TradingAccountID: "account-a", ClientOrderID: "old-target-order",
			ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
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
		TradingAccountID: "account-b", Enabled: true, Priority: 2,
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
			Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT",
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

func TestModernSessionTargetCanResume(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	acceptModernSessionTarget(t, tradeStore, "session-1")
	account, _, err := service.Resume(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
	require.Equal(t, fence, account.AuthFence)
}

func TestIdempotentClaimSessionPreservesTarget(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	want := acceptModernSessionTarget(t, tradeStore, "session-1")
	_, actualFence, err := service.ClaimSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence)
	require.NoError(t, err)
	require.Equal(t, fence, actualFence)
	got, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestIdempotentRebindSessionPreservesTarget(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	_, fence, err := service.RebindSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence, "instance-1", "session-2")
	require.NoError(t, err)
	want := acceptModernSessionTarget(t, tradeStore, "session-2")
	_, actualFence, err := service.RebindSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", fence, "instance-1", "session-2")
	require.NoError(t, err)
	require.Equal(t, fence, actualFence)
	got, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestSessionServiceRollsBackWhenTargetDeleteFails(t *testing.T) {
	for _, operation := range []string{"claim", "rebind"} {
		t.Run(operation, func(t *testing.T) {
			service, tradeStore, fence := modernSessionFixture(t)
			ctx := context.Background()
			target := acceptModernSessionTarget(t, tradeStore, "session-1")
			require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
				return tx.InsertTargetReceipt(store.TargetReceiptRecord{
					SpaceID: target.SpaceID, LogicalAccountID: target.LogicalAccountID, TargetID: target.TargetID,
					InstanceID: target.InstanceID, SessionID: target.SessionID, StrategyID: target.StrategyID,
					BarEndTime: target.BarEndTime, EffectiveAt: target.EffectiveAt, ValidUntil: target.ValidUntil,
					RequestHash: "hash", WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: "[]", AcceptedAt: target.AcceptedAt,
				})
			}))
			if operation == "claim" {
				// Leave the old target in place to exercise the service's claim cleanup.
				require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
					return tx.ReleaseLogicalAccountSession("space-1", "logical-1", "instance-1", "session-1", fence)
				}))
			}
			before, err := tradeStore.GetLogicalAccount(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			receipt, err := tradeStore.GetTargetReceipt(ctx, "space-1", target.TargetID)
			require.NoError(t, err)
			require.NoError(t, tradeStore.DBForTest().Exec(`CREATE TRIGGER fail_target_delete
				AFTER DELETE ON t_logical_account_targets
				BEGIN SELECT RAISE(ABORT, 'injected target delete failure'); END`).Error)
			transition := func() (store.LogicalAccountRecord, string, error) {
				if operation == "claim" {
					return service.ClaimSession(ctx, "space-1", "logical-1", "instance-2", "session-2", before.AuthFence)
				}
				return service.RebindSession(ctx, "space-1", "logical-1", "instance-1", "session-1", before.AuthFence, "instance-2", "session-2")
			}
			_, _, err = transition()
			require.ErrorContains(t, err, "injected target delete failure")
			current, err := tradeStore.GetLogicalAccount(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, before, current)
			currentTarget, err := tradeStore.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, target, currentTarget)
			currentReceipt, err := tradeStore.GetTargetReceipt(ctx, "space-1", target.TargetID)
			require.NoError(t, err)
			require.Equal(t, receipt, currentReceipt)
			require.NoError(t, tradeStore.DBForTest().Exec("DROP TRIGGER fail_target_delete").Error)
			account, newFence, err := transition()
			require.NoError(t, err)
			require.NotEqual(t, before.AuthFence, newFence)
			require.Equal(t, "instance-2", account.OwnerInstanceID)
			require.Equal(t, "session-2", account.OwnerSessionID)
			require.Equal(t, newFence, account.AuthFence)
			current, err = tradeStore.GetLogicalAccount(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, account, current)
			_, err = tradeStore.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			currentReceipt, err = tradeStore.GetTargetReceipt(ctx, "space-1", target.TargetID)
			require.NoError(t, err)
			require.Equal(t, receipt, currentReceipt)
		})
	}
}

func TestSessionTransitionClearsTargetAndRejectsDelayedManagement(t *testing.T) {
	service, tradeStore, oldFence := modernSessionFixture(t)
	ctx := context.Background()
	oldTarget := acceptModernSessionTarget(t, tradeStore, "session-1")
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		return tx.InsertTargetReceipt(store.TargetReceiptRecord{
			SpaceID: oldTarget.SpaceID, LogicalAccountID: oldTarget.LogicalAccountID, TargetID: oldTarget.TargetID,
			InstanceID: oldTarget.InstanceID, SessionID: oldTarget.SessionID, StrategyID: oldTarget.StrategyID,
			BarEndTime: oldTarget.BarEndTime, EffectiveAt: oldTarget.EffectiveAt, ValidUntil: oldTarget.ValidUntil,
			RequestHash: "hash", WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: "[]", AcceptedAt: oldTarget.AcceptedAt,
		})
	}))
	beforeReceipt, err := tradeStore.GetTargetReceipt(ctx, "space-1", oldTarget.TargetID)
	require.NoError(t, err)
	account, fence, err := service.RebindSession(ctx, "space-1", "logical-1", "instance-1", "session-1", oldFence, "instance-1", "session-2")
	require.NoError(t, err)
	require.NotEqual(t, oldFence, fence)
	require.Equal(t, "PAUSED", account.AutomationState)
	_, err = tradeStore.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	receipt, err := tradeStore.GetTargetReceipt(ctx, "space-1", oldTarget.TargetID)
	require.NoError(t, err)
	require.Equal(t, beforeReceipt, receipt)
	want := acceptModernSessionTarget(t, tradeStore, "session-2")
	for _, operation := range []string{"release", "rebind", "claim", "release-old-identity-current-fence", "rebind-old-identity-current-fence"} {
		t.Run(operation, func(t *testing.T) {
			var err error
			switch operation {
			case "release":
				err = service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", oldFence)
			case "rebind":
				_, _, err = service.RebindSession(ctx, "space-1", "logical-1", "instance-1", "session-1", oldFence, "instance-1", "session-2")
			case "claim":
				_, _, err = service.ClaimSession(ctx, "space-1", "logical-1", "instance-1", "session-2", oldFence)
			case "release-old-identity-current-fence":
				err = service.ReleaseSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence)
			case "rebind-old-identity-current-fence":
				_, _, err = service.RebindSession(ctx, "space-1", "logical-1", "instance-1", "session-1", fence, "instance-1", "session-3")
			}
			require.ErrorIs(t, err, store.ErrConflict)
			current, err := tradeStore.GetLogicalAccount(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, account, current)
			currentTarget, err := tradeStore.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, want, currentTarget)
		})
	}
}

func TestModernResumeWithoutTargetWaitsForNextTarget(t *testing.T) {
	service, tradeStore, _ := modernSessionFixture(t)
	account, _, err := service.Resume(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
	_, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestModernResumeIgnoresExpiredTargetMetadata(t *testing.T) {
	service, tradeStore, _ := modernSessionFixture(t)
	want := acceptModernSessionTarget(t, tradeStore, "session-1")
	service.Now = func() time.Time { return time.UnixMilli(want.ValidUntil) }
	service.MaxSnapshotAge = 2 * time.Hour
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SWAP", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDC", Linear: true, ContractValue: "0.001",
			ContractValueAsset: "BTC", ExchangeQuantityStep: "1",
			MinExchangeQuantity: "1", PriceTick: "0.1", Status: "TRADING",
		})
	}))
	account, _, err := service.Resume(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
	got, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, want, got)
	executor := nonTradingExecutor(tradeStore, service.Now)
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusExpired, result.Status)
	got, err = tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	want.Status = targetapp.StatusExpired
	require.Equal(t, want.TargetID, got.TargetID)
	require.Equal(t, want.Status, got.Status)
}

func TestPausedModernTargetDoesNotTrade(t *testing.T) {
	service, tradeStore, _ := modernSessionFixture(t)
	want := acceptModernSessionTarget(t, tradeStore, "session-1")
	executor := nonTradingExecutor(tradeStore, service.Now)
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, targetapp.StatusPaused, result.Status)
	require.Empty(t, result.Action)
	got, err := tradeStore.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, want, got)
	orders, _, err := tradeStore.ListOrders(context.Background(), "space-1", store.OrderQuery{LogicalAccountID: "logical-1"})
	require.NoError(t, err)
	require.Empty(t, orders)
}

func nonTradingExecutor(tradeStore *store.Store, now func() time.Time) *targetapp.Executor {
	// Any quote or order call panics through the nil embedded interface:
	// these guards must return before touching an external trading dependency.
	return &targetapp.Executor{
		Store: tradeStore, Now: now,
		Orders: struct{ targetapp.OrderService }{},
		Prices: struct{ targetapp.PriceSource }{},
	}
}

func TestModernReadinessRejectsDifferentSession(t *testing.T) {
	service, tradeStore, fence := modernSessionFixture(t)
	acceptModernSessionTarget(t, tradeStore, "session-1")
	// Store-only rebind deliberately leaves a stale target to exercise readiness.
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		_, changed, err := tx.RebindLogicalAccountSession("space-1", "logical-1", "instance-1", "session-1", fence, "instance-1", "session-2")
		require.True(t, changed)
		return err
	}))
	readiness, err := service.Readiness(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.False(t, readiness.Ready)
	require.Contains(t, readiness.Reasons, "target session does not own logical account")
}

func modernSessionFixture(t *testing.T) (*Service, *store.Store, string) {
	t.Helper()
	service, tradeStore := logicalAccountServiceFixture(t)
	now := time.Now().UTC()
	service.Now = func() time.Time { return now }
	physical, err := tradeStore.GetTradingAccountByID(context.Background(), "account-a")
	require.NoError(t, err)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync("space-1", "account-a", store.TradingAccountSyncState{
			Ready: true, LeverageSettings: physical.LeverageSettings, Snapshot: physical.Snapshot,
			SnapshotSourceTime: now.UnixMilli(), LastSyncAt: now.UnixMilli(), LastReadyAt: now.UnixMilli(),
		})
	}))
	account, err := tradeStore.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	_, fence, err := service.ClaimSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", account.AuthFence)
	require.NoError(t, err)
	return service, tradeStore, fence
}

func acceptModernSessionTarget(t *testing.T, tradeStore *store.Store, session string) store.LogicalAccountTargetRecord {
	t.Helper()
	now := time.Now().UTC()
	target, accepted, err := tradeStore.AcceptLogicalAccountTarget(context.Background(), store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-" + session,
		InstanceID: "instance-1", SessionID: session, StrategyID: "strategy-1",
		BarEndTime: now.Add(-time.Second).UnixMilli(), EffectiveAt: now.Add(-time.Second).UnixMilli(), ValidUntil: now.Add(time.Hour).UnixMilli(),
		Status: "PENDING", AcceptedAt: now.UnixMilli(),
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "1"}},
	})
	require.NoError(t, err)
	require.True(t, accepted)
	return target
}

func logicalAccountServiceFixture(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	now := time.UnixMilli(2_000).UTC()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, account := range []store.TradingAccountRecord{
			logicalFixtureAccount("account-a"),
			logicalFixtureAccount("account-b"),
		} {
			if err := tx.CreateTradingAccount(account); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", ExecutionMode: "LIVE",
			MarketType: "SWAP", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TradingAccountID: "account-a", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT",
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

func logicalFixtureAccount(id string) store.TradingAccountRecord {
	return store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: id, Name: id,
		Exchange: "BINANCE", MarketType: "SWAP",
		ExecutionMode: "LIVE", Environment: "TESTNET", CredentialSecretID: "secret-1",
		SettlementAsset: "USDT", MarginMode: "CROSS",
		Status: "ENABLED", Ready: true,
		LeverageSettings: store.LeverageSettings{"BTCUSDT": "5"},
		Snapshot: store.TradingAccountSnapshot{
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
	account, err := tradeStore.GetTradingAccountByID(context.Background(), accountID)
	require.NoError(t, err)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync(
			account.SpaceID, account.TradingAccountID,
			store.TradingAccountSyncState{
				Ready: ready, LeverageSettings: account.LeverageSettings,
				Snapshot:           account.Snapshot,
				SnapshotSourceTime: account.SnapshotSourceTime,
				LastSyncAt:         1_900, LastReadyAt: 1_900,
			},
		)
	}))
}
