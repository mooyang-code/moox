package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogicalAccountMembershipEnforcesHomogeneityAndOneEnabledGroup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	second := testAccount()
	second.ExchangeAccountID = "account-2"
	second.Name = "second"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateExchangeAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.CreateExchangeAccount(second); err != nil {
			return err
		}
		for _, account := range []LogicalAccountRecord{
			{
				SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "one",
				ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
				AutomationState: "PAUSED", PauseReason: "configure",
			},
			{
				SpaceID: "space-1", LogicalAccountID: "logical-2", Name: "two",
				ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
				AutomationState: "PAUSED", PauseReason: "configure",
			},
		} {
			if err := tx.CreateLogicalAccount(account); err != nil {
				return err
			}
		}
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			ExchangeAccountID: "account-1", Enabled: true,
		})
	}))

	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2",
			ExchangeAccountID: "account-1", Enabled: true,
		})
	})
	require.ErrorIs(t, err, ErrConflict)

	err = s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2",
			ExchangeAccountID: "account-1", Enabled: false,
		})
	})
	require.NoError(t, err)
}

func TestLogicalAccountMembershipRejectsInhomogeneousAccount(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	swap := testAccount()
	swap.ExchangeAccountID = "swap-1"
	swap.Name = "swap"
	swap.MarketType = "SWAP"
	swap.MarginMode = "CROSS"
	err := s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateExchangeAccount(swap); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "spot",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			ExchangeAccountID: "swap-1", Enabled: true,
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

func TestLogicalAccountMembershipChangeRequiresPause(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateExchangeAccount(testAccount()); err != nil {
			return err
		}
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "active",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "ACTIVE",
		})
	}))
	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			ExchangeAccountID: "account-1", Enabled: true,
		})
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

func TestLogicalAccountOwnerAndAutomationRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "main",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "new",
		}); err != nil {
			return err
		}
		if err := tx.SetLogicalAccountOwner("space-1", "logical-1", "runner-1"); err != nil {
			return err
		}
		return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
	}))
	got, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "runner-1", got.OwnerRunnerID)
	require.Equal(t, "ACTIVE", got.AutomationState)
	require.Empty(t, got.PauseReason)
}
