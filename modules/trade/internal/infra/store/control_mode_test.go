package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTargetReceiptPreservesAccountReadFailure(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	injected := errors.New("injected account read failure")
	require.NoError(t, s.db.Callback().Query().Before("gorm:query").Register("fail_control_mode_read", func(tx *gorm.DB) {
		if tx.Statement.Table == "t_logical_accounts" {
			tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { require.NoError(t, s.db.Callback().Query().Remove("fail_control_mode_read")) })
	_, accepted, err := s.AcceptLogicalAccountTargetWithReceipt(context.Background(), validLogicalAccountTarget(), TargetReceiptRecord{})
	require.False(t, accepted)
	require.ErrorIs(t, err, injected)
	require.NotErrorIs(t, err, ErrTargetAuthorization)
}

func TestManualControlModeRejectsStrategyMutations(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{SpaceID: "space", LogicalAccountID: "manual", Name: "manual", ControlMode: "MANUAL", ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "manual"})
	}))
	before, err := s.GetLogicalAccount(ctx, "space", "manual")
	require.NoError(t, err)
	require.Equal(t, "MANUAL", before.ControlMode)
	cases := map[string]func(*Tx) error{
		"set owner":   func(tx *Tx) error { return tx.SetLogicalAccountOwnerGeneration("space", "manual", "runner") },
		"claim owner": func(tx *Tx) error { return tx.TryClaimLogicalAccountOwner("space", "manual", "runner") },
		"rebind owner": func(tx *Tx) error {
			_, err := tx.RebindLogicalAccountOwner("space", "manual", "runner", "key")
			return err
		},
		"claim session": func(tx *Tx) error {
			_, _, err := tx.ClaimLogicalAccountSession("space", "manual", "instance", "session", before.AuthFence)
			return err
		},
		"rebind session": func(tx *Tx) error {
			_, _, err := tx.RebindLogicalAccountSession("space", "manual", "old", "old", before.AuthFence, "instance", "session")
			return err
		},
		"activate": func(tx *Tx) error { return tx.SetLogicalAccountAutomation("space", "manual", "ACTIVE", "") },
	}
	for name, operation := range cases {
		t.Run(name, func(t *testing.T) {
			require.Error(t, s.Transaction(ctx, operation))
			after, err := s.GetLogicalAccount(ctx, "space", "manual")
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestManualControlRejectsTargetReplay(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	target := validLogicalAccountTarget()
	_, _, err := s.AcceptLogicalAccountTarget(ctx, target)
	require.NoError(t, err)
	require.NoError(t, s.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode = 'MANUAL'").Error)
	_, _, err = s.AcceptLogicalAccountTarget(ctx, target)
	require.Error(t, err)
	receipt := TargetReceiptRecord{SpaceID: "space-1", TargetID: target.TargetID, LogicalAccountID: "logical-1", RunnerID: "runner-1", CommandSequence: 1, RequestHash: "hash", SignalTime: 1, WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: "[]", AcceptedAt: 1}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.InsertTargetReceipt(receipt) }))
	_, _, err = s.AcceptLogicalAccountTargetWithReceipt(ctx, target, receipt)
	require.ErrorIs(t, err, ErrTargetAuthorization)
}

func TestCreateControlModesPersistAndRejectUnknown(t *testing.T) {
	s := openTestStore(t)
	for _, mode := range []string{"", "STRATEGY", "MANUAL", "invalid"} {
		err := s.Transaction(context.Background(), func(tx *Tx) error {
			return tx.CreateLogicalAccount(LogicalAccountRecord{SpaceID: "space", LogicalAccountID: "account-" + mode, Name: "account-" + mode, ControlMode: mode, ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "created"})
		})
		if mode == "invalid" {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		got, err := s.GetLogicalAccount(context.Background(), "space", "account-"+mode)
		require.NoError(t, err)
		want := mode
		if want == "" {
			want = "STRATEGY"
		}
		require.Equal(t, want, got.ControlMode)
	}
}

func TestManualControlRejectsIdempotentStrategyFallbacks(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.RebindLogicalAccountOwner("space-1", "logical-1", "runner-1", "known-key")
		return err
	}))
	require.NoError(t, s.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode = 'MANUAL', c_owner_instance_id = 'instance', c_owner_session_id = 'session'").Error)
	before, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Error(t, s.Transaction(ctx, func(tx *Tx) error {
		_, err := tx.RebindLogicalAccountOwner("space-1", "logical-1", "runner-1", "known-key")
		return err
	}))
	require.Error(t, s.Transaction(ctx, func(tx *Tx) error {
		_, _, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "instance", "session", before.AuthFence)
		return err
	}))
	require.Error(t, s.Transaction(ctx, func(tx *Tx) error {
		_, _, err := tx.RebindLogicalAccountSession("space-1", "logical-1", "old", "old", before.AuthFence, "instance", "session")
		return err
	}))
	after, err := s.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, before, after)
}
