package operator

import (
	"context"
	"errors"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestFlattenFreshSyncsBeforeCancelAndClose(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.orders.nextID = ""
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.order(t, activeOrder(fixture, "active-order", "EXTERNAL"))

	result, err := fixture.service().FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Equal(t, []string{
		"sync:account-a",
		"cancel:active-order",
		"sync:account-a",
		"place:" + flattenClientOrderIDForSpec(
			"flatten-1", "account-a", "BTCUSDT", exchange.SideSell,
			shared.MustDecimal("2"),
		),
		"submit:child-account-a-BTCUSDT",
		"sync:account-a",
		"sync:account-b",
		"sync:account-b",
		"sync:account-b",
	}, fixture.trace)
	require.Equal(t, "PARTIAL", result.Action.Status)
	require.Len(t, fixture.orders.specs, 1)
	spec := fixture.orders.specs[0]
	require.Equal(t, "account-a", spec.ExchangeAccountID)
	require.Equal(t, exchange.SideSell, spec.Side)
	require.Equal(t, "2", spec.Quantity.String())
	require.True(t, spec.ReducePositionOnly)
	require.Equal(t, orderdomain.OwnerOperator, spec.Owner.Type)
	require.Equal(t, "flatten-1", spec.Owner.OwnerID)
}

func TestFlattenUsesPausedAutomationStateAndRunningAction(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	fixture.syncer.onSync = func(ctx context.Context, accountID string, call int) error {
		if call != 1 || accountID != "account-a" {
			return nil
		}
		account, err := fixture.store.GetLogicalAccount(ctx, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, "PAUSED", account.AutomationState)
		action, err := fixture.store.GetOperatorAction(ctx, "space-1", "flatten-1")
		require.NoError(t, err)
		require.Equal(t, "RUNNING", action.Status)
		return nil
	}

	_, err := fixture.service().FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
}

func TestFlattenCompletedReplayDoesNotPauseAfterResume(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	command := FlattenCommand{
		SpaceID: "space-1", ActionID: "flatten-1",
		LogicalAccountID: "logical-1", Reason: "close risk",
	}
	_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "ACTIVE", "",
		)
	}))

	_, err = fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)

	account, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
}

func TestFlattenWaitsForCancellationConfirmation(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.order(t, activeOrder(fixture, "active-order", "TARGET"))
	fixture.orders.leaveOpen = map[string]bool{"active-order": true}

	result, err := fixture.service().FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Empty(t, fixture.orders.specs)
	require.Equal(t, "PARTIAL", result.Action.Status)
	require.Contains(t, result.Accounts[0].Error, "cancellation is not confirmed")
}

func TestFlattenSkipsStaleFailedAccountAndContinuesOthers(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.orders.nextID = ""
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.position(t, "account-b", "BTC-USDT-SWAP", "-3")
	fixture.syncer.fail = map[string]error{"account-a": errors.New("sync unavailable")}

	result, err := fixture.service().FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, "account-b", fixture.orders.specs[0].ExchangeAccountID)
	require.Equal(t, exchange.SideBuy, fixture.orders.specs[0].Side)
	require.Equal(t, "3", fixture.orders.specs[0].Quantity.String())
	require.Equal(t, "PARTIAL", result.Action.Status)
	require.Equal(t, "sync unavailable", result.Accounts[0].Error)
}

func TestFlattenReportsPartialRemainingPositionsAndEndsPaused(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "0.0005")

	result, err := fixture.service().FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Equal(t, "PARTIAL", result.Action.Status)
	require.NotEmpty(t, result.Accounts[0].Remaining)
	require.Contains(t, result.Accounts[0].Remaining[0].Reason, "minimum")
	account, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
}

func TestFlattenRetriesSameActionWithoutDuplicateChildren(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "existing-child",
		ExchangeAccountID: "account-a",
		ClientOrderID:     flattenClientOrderID("flatten-1", "account-a", "BTCUSDT"),
		Symbol:            "BTCUSDT", OrderType: "MARKET", Side: "SELL",
		PositionSide: "NET", Quantity: "2", ReferencePrice: "100",
		ReferencePriceAt: fixture.now.UnixMilli(), ReduceOnly: true,
		OwnerType: "OPERATOR", OwnerID: "flatten-1",
		LogicalAccountID: "logical-1", State: "PENDING", Version: 1,
	})
	requestJSON, err := flattenRequestJSON(FlattenCommand{
		SpaceID: "space-1", ActionID: "flatten-1",
		LogicalAccountID: "logical-1", Reason: "close risk",
	})
	require.NoError(t, err)
	_, _, err = fixture.store.CreateOperatorAction(
		context.Background(),
		store.OperatorActionRecord{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", ActionType: "FLATTEN",
			Reason: "close risk", RequestJSON: requestJSON, Status: "RUNNING",
		},
	)
	require.NoError(t, err)

	_, err = fixture.service().FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Empty(t, fixture.orders.specs)
	require.Equal(t, 1, fixture.orders.submitCalls)
	require.Contains(t, fixture.trace, "submit:existing-child")
}

func TestFlattenIncludesDisabledMembersAndKeepsSpotSettlementCash(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	fixture.orders.nextID = ""
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "PAUSED", "configure",
		); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			ExchangeAccountID: "account-b", Enabled: false, Priority: 2,
		}); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "ACTIVE", "",
		)
	}))
	account, err := fixture.store.GetExchangeAccountByID(context.Background(), "account-b")
	require.NoError(t, err)
	account.Snapshot.Balances = []store.AssetBalance{
		{Asset: "USDT", Available: "500", Total: "500"},
		{Asset: "BTC", Available: "1", Total: "1"},
	}
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateExchangeAccountSync(
			account.SpaceID, account.ExchangeAccountID,
			store.ExchangeAccountSyncState{
				Ready: true, Snapshot: account.Snapshot,
				SnapshotSourceTime: fixture.now.UnixMilli(),
				LastSyncAt:         fixture.now.UnixMilli(),
			},
		)
	}))

	_, err = fixture.service().FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Contains(t, fixture.trace, "sync:account-b")
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, "account-b", fixture.orders.specs[0].ExchangeAccountID)
	require.Equal(t, exchange.SideSell, fixture.orders.specs[0].Side)
	require.Equal(t, "1", fixture.orders.specs[0].Quantity.String())
}

func TestFlattenFailsWhenNoMemberCanExecute(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.syncer.fail = map[string]error{
		"account-a": errors.New("a failed"),
		"account-b": errors.New("b failed"),
	}

	result, err := fixture.service().FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Equal(t, "FAILED", result.Action.Status)
	require.Empty(t, fixture.orders.specs)
}

func activeOrder(
	fixture *operatorFixture,
	orderID string,
	ownerType string,
) store.OrderRecord {
	record := store.OrderRecord{
		SpaceID: "space-1", OrderID: orderID,
		ExchangeAccountID: "account-a", ClientOrderID: orderID,
		Symbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
		PositionSide: map[bool]string{
			true: "NET", false: "",
		}[fixture.market == exchange.MarketTypeSwap],
		Quantity: "1", ReferencePrice: "100",
		ReferencePriceAt: fixture.now.UnixMilli(),
		OwnerType:        ownerType, OwnerID: "owner-1",
		LogicalAccountID: "logical-1", State: "OPEN", Version: 1,
	}
	if ownerType == "TARGET" {
		record.RunnerID = "runner-1"
	}
	return record
}

func TestFlattenDoesNotNetOpposingPhysicalAccounts(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.orders.nextID = ""
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.position(t, "account-b", "BTC-USDT-SWAP", "-2")

	_, err := fixture.service().FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 2)
	require.Equal(t, shared.MustDecimal("2").String(), fixture.orders.specs[0].Quantity.String())
	require.Equal(t, exchange.SideSell, fixture.orders.specs[0].Side)
	require.Equal(t, shared.MustDecimal("2").String(), fixture.orders.specs[1].Quantity.String())
	require.Equal(t, exchange.SideBuy, fixture.orders.specs[1].Side)
}

func TestFlattenRetriesDelayedFillUntilPositionIsZero(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.orders.nextID = ""
	fixture.orders.leaveOpen = map[string]bool{
		"child-account-a-BTCUSDT": true,
	}
	fixture.position(t, "account-a", "BTCUSDT", "2")
	fixture.syncer.onSync = func(ctx context.Context, accountID string, call int) error {
		if accountID != "account-a" || call != 4 {
			return nil
		}
		require.NoError(t, setOperatorOrderState(
			ctx,
			fixture.store,
			"space-1",
			"child-account-a-BTCUSDT",
			"FILLED",
		))
		fixture.position(t, "account-a", "BTCUSDT", "0")
		return nil
	}
	service := fixture.service()
	service.FlattenMaxAttempts = 2

	result, err := service.FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.Len(t, fixture.orders.specs, 1)
	require.Empty(t, result.Accounts[0].Remaining)
}

func TestFlattenRetryStopsAtDeadline(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.orders.nextID = ""
	fixture.orders.leaveOpen = map[string]bool{
		"child-account-a-BTCUSDT": true,
	}
	fixture.position(t, "account-a", "BTCUSDT", "2")
	service := fixture.service()
	service.FlattenMaxAttempts = 2
	service.FlattenRetryInterval = time.Second
	service.FlattenTimeout = 20 * time.Millisecond

	started := time.Now()
	result, err := service.FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.Less(t, time.Since(started), 500*time.Millisecond)
	require.Equal(t, "PARTIAL", result.Action.Status)
	require.Equal(t, 3, fixture.syncer.callsFor("account-a"))
}

func TestFlattenRecoveryCallsReceiveDeadlineContext(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSpot)
	allCallsBounded := true
	fixture.syncer.onSync = func(ctx context.Context, _ string, _ int) error {
		if _, ok := ctx.Deadline(); !ok {
			allCallsBounded = false
		}
		return nil
	}
	service := fixture.service()
	service.FlattenTimeout = time.Second

	_, err := service.FlattenLogicalAccount(
		context.Background(),
		FlattenCommand{
			SpaceID: "space-1", ActionID: "flatten-1",
			LogicalAccountID: "logical-1", Reason: "close risk",
		},
	)

	require.NoError(t, err)
	require.True(t, allCallsBounded)
}

func TestFlattenPartialReplayContinuesSameActionWithoutDuplicateChild(t *testing.T) {
	fixture := newOperatorFixture(t, exchange.MarketTypeSwap)
	fixture.orders.nextID = ""
	fixture.orders.leaveOpen = map[string]bool{
		"child-account-a-BTCUSDT": true,
	}
	fixture.position(t, "account-a", "BTCUSDT", "2")
	command := FlattenCommand{
		SpaceID: "space-1", ActionID: "flatten-1",
		LogicalAccountID: "logical-1", Reason: "close risk",
	}
	first, err := fixture.service().FlattenLogicalAccount(
		context.Background(), command,
	)
	require.NoError(t, err)
	require.Equal(t, "PARTIAL", first.Action.Status)
	require.NoError(t, setOperatorOrderState(
		context.Background(),
		fixture.store,
		"space-1",
		"child-account-a-BTCUSDT",
		"FILLED",
	))
	fixture.position(t, "account-a", "BTCUSDT", "0")

	replayed, err := fixture.service().FlattenLogicalAccount(
		context.Background(), command,
	)

	require.NoError(t, err)
	require.Equal(t, "COMPLETED", replayed.Action.Status)
	require.Len(t, fixture.orders.specs, 1)
}
