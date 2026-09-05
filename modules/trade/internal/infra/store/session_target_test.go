package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
		fence, err = tx.ClaimLogicalAccountSession("space-1", "logical-1", "instance-1", "session-1", account.AuthFence)
		return err
	}))
	require.NotEmpty(t, fence)

	staleErr := s.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "instance-2", "session-2", fence)
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
		_, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "instance-2", "session-2", "")
		return err
	})
	require.ErrorIs(t, missingFenceErr, ErrInvalidRecord)
}
