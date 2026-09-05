package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSessionTransitionRollsBackWhenTargetDeleteFails(t *testing.T) {
	for _, operation := range []string{"claim", "rebind"} {
		t.Run(operation, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
				return tx.CreateLogicalAccount(LogicalAccountRecord{
					SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "main",
					OwnerInstanceID: "instance-1", OwnerSessionID: "session-1",
					ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
					AutomationState: "PAUSED", PauseReason: "setup",
				})
			}))
			now := time.Now().UTC()
			bar := now.Add(-time.Second).UnixMilli()
			target, accepted, err := s.AcceptLogicalAccountTarget(ctx, LogicalAccountTargetRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-1",
				InstanceID: "instance-1", SessionID: "session-1", StrategyID: "strategy-1",
				BarEndTime: bar, EffectiveAt: bar, ValidUntil: now.Add(time.Hour).UnixMilli(),
				Targets: []InstrumentTarget{}, Status: "PENDING", AcceptedAt: now.UnixMilli(),
			})
			require.NoError(t, err)
			require.True(t, accepted)
			require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
				return tx.InsertTargetReceipt(TargetReceiptRecord{
					SpaceID: target.SpaceID, LogicalAccountID: target.LogicalAccountID, TargetID: target.TargetID,
					InstanceID: target.InstanceID, SessionID: target.SessionID, StrategyID: target.StrategyID,
					BarEndTime: target.BarEndTime, EffectiveAt: target.EffectiveAt, ValidUntil: target.ValidUntil,
					RequestHash: "hash", WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: "[]", AcceptedAt: target.AcceptedAt,
				})
			}))
			if operation == "claim" {
				// A store-only release leaves the prior target for the next claim to clear.
				require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
					account, err := tx.GetLogicalAccount("space-1", "logical-1")
					if err != nil {
						return err
					}
					return tx.ReleaseLogicalAccountSession("space-1", "logical-1", "instance-1", "session-1", account.AuthFence)
				}))
			}
			before, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			receipt, err := s.GetTargetReceipt(ctx, "space-1", "target-1")
			require.NoError(t, err)
			require.NoError(t, s.db.Exec(`CREATE TRIGGER fail_target_delete
				AFTER DELETE ON t_logical_account_targets
				BEGIN SELECT RAISE(ABORT, 'injected target delete failure'); END`).Error)
			transition := func(tx *Tx) error {
				var fence string
				var changed bool
				var err error
				if operation == "claim" {
					fence, changed, err = tx.ClaimLogicalAccountSession("space-1", "logical-1", "instance-2", "session-2", before.AuthFence)
				} else {
					fence, changed, err = tx.RebindLogicalAccountSession("space-1", "logical-1", "instance-1", "session-1", before.AuthFence, "instance-2", "session-2")
				}
				require.NoError(t, err)
				require.True(t, changed)
				require.NotEqual(t, before.AuthFence, fence)
				current, err := tx.GetLogicalAccount("space-1", "logical-1")
				require.NoError(t, err)
				require.Equal(t, "instance-2", current.OwnerInstanceID)
				require.Equal(t, "session-2", current.OwnerSessionID)
				require.Equal(t, fence, current.AuthFence)
				return tx.DeleteLogicalAccountTarget("space-1", "logical-1")
			}
			err = s.Transaction(ctx, transition)
			require.ErrorContains(t, err, "injected target delete failure")
			current, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, before, current)
			currentTarget, err := s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, target, currentTarget)
			currentReceipt, err := s.GetTargetReceipt(ctx, "space-1", "target-1")
			require.NoError(t, err)
			require.Equal(t, receipt, currentReceipt)
			require.NoError(t, s.db.Exec("DROP TRIGGER fail_target_delete").Error)
			require.NoError(t, s.Transaction(ctx, transition))
			current, err = s.GetLogicalAccount(ctx, "space-1", "logical-1")
			require.NoError(t, err)
			require.Equal(t, "instance-2", current.OwnerInstanceID)
			require.Equal(t, "session-2", current.OwnerSessionID)
			require.NotEqual(t, before.AuthFence, current.AuthFence)
			_, err = s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
			currentReceipt, err = s.GetTargetReceipt(ctx, "space-1", "target-1")
			require.NoError(t, err)
			require.Equal(t, receipt, currentReceipt)
		})
	}
}

func TestSessionAuthorizationCASAndTargetPeriod(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "main",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "setup",
		})
	}))

	var fence string
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		account, err := tx.GetLogicalAccount("space-1", "logical-1")
		if err != nil {
			return err
		}
		var changed bool
		fence, changed, err = tx.ClaimLogicalAccountSession("space-1", "logical-1", "instance-1", "session-1", account.AuthFence)
		require.True(t, changed)
		return err
	}))
	require.NotEmpty(t, fence)
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		actual, changed, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "instance-1", "session-1", fence)
		require.Equal(t, fence, actual)
		require.False(t, changed)
		return err
	}))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		actual, changed, err := tx.RebindLogicalAccountSession("space-1", "logical-1", "instance-1", "session-1", fence, "instance-1", "session-1")
		require.Equal(t, fence, actual)
		require.False(t, changed)
		return err
	}))

	staleErr := s.Transaction(ctx, func(tx *Tx) error {
		_, changed, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "instance-2", "session-2", fence)
		require.False(t, changed)
		return err
	})
	require.ErrorIs(t, staleErr, ErrConflict)

	now := time.Now().UTC()
	bar := now.Add(-time.Second).UnixMilli()
	validUntil := now.Add(time.Minute).UnixMilli()
	target := LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-1",
		InstanceID: "instance-1", SessionID: "session-1", StrategyID: "strategy-1",
		BarEndTime: bar, EffectiveAt: bar, ValidUntil: validUntil,
		Targets: []InstrumentTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"}},
		Status:  "PENDING", AcceptedAt: now.UnixMilli(),
	}
	current, accepted, err := s.AcceptLogicalAccountTarget(ctx, target)
	require.NoError(t, err)
	require.True(t, accepted)
	require.Equal(t, target.InstanceID, current.InstanceID)
	require.Equal(t, target.SessionID, current.SessionID)

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.ReleaseLogicalAccountSession("space-1", "logical-1", "instance-1", "session-1", fence)
	}))
	account, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, account.OwnerInstanceID)
	missingFenceErr := s.Transaction(ctx, func(tx *Tx) error {
		_, changed, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "instance-2", "session-2", "")
		require.False(t, changed)
		return err
	})
	require.ErrorIs(t, missingFenceErr, ErrInvalidRecord)
}
