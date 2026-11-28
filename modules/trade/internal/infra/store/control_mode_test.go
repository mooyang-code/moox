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
	_, accepted, err := s.AcceptLogicalAccountTargetWithHistoryLocked(context.Background(), TargetHistoryRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-1",
	})
	require.False(t, accepted)
	require.ErrorIs(t, err, injected)
	require.NotErrorIs(t, err, ErrTargetAuthorization)
}

func TestManualControlModePauseNoOpResumeRejectsAndMemberEditable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "manual", Name: "manual",
			ControlMode: "MANUAL", Environment: "PAPER", SettlementAsset: "USDT",
		})
	}))
	before, err := s.GetLogicalAccount(ctx, "space-1", "manual")
	require.NoError(t, err)
	require.Equal(t, "MANUAL", before.ControlMode)
	require.Empty(t, before.AutomationState())
	require.Empty(t, before.PauseSource)
	require.Empty(t, before.PauseReason)
	require.False(t, before.IsPaused())
	require.False(t, before.IsActive())
	require.True(t, before.CanEditMembers())

	// 自主下单组合不带暂停状态：暂停为空操作且返回成功，条件暂停返回 false 且不改写，恢复被拒绝。
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PauseLogicalAccount("space-1", "manual", UserPause("手动暂停"))
	}))
	var changed bool
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		var pauseErr error
		changed, pauseErr = tx.PauseLogicalAccountIf("space-1", "manual", Pause{}, SystemPause("逐仓"))
		return pauseErr
	}))
	require.False(t, changed)
	require.Error(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.ResumeLogicalAccount("space-1", "manual")
	}))

	after, err := s.GetLogicalAccount(ctx, "space-1", "manual")
	require.NoError(t, err)
	require.Equal(t, before, after)

	// 自主下单组合随时允许增删成员，无需暂停。
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "manual", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		})
	}))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.DeleteLogicalAccountMember("space-1", "manual", "account-1")
	}))
}

func TestManualControlRejectsTargetReplay(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	target := validLogicalAccountTarget()
	_, _, err := acceptTargetForTest(ctx, s, target)
	require.NoError(t, err)
	require.NoError(t, s.DBForTest().Exec("UPDATE t_logical_accounts SET c_control_mode = 'MANUAL', c_pause_source = '', c_pause_reason = '', c_margin_reduce_state = ''").Error)
	_, _, err = acceptTargetForTest(ctx, s, target)
	require.Error(t, err)
	receipt := TargetHistoryRecord{SpaceID: "space-1", TargetID: target.TargetID, LogicalAccountID: "logical-1", InstanceID: target.InstanceID, SessionID: target.SessionID, StrategyID: target.StrategyID, SessionStartedAt: target.SessionStartedAt, BarEndTime: target.BarEndTime, ValidUntil: target.ValidUntil, RequestHash: "hash", SignalTime: 1, WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: "[]", RecordedAt: 1}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.InsertTargetHistory(receipt) }))
	_, _, err = s.AcceptLogicalAccountTargetWithHistoryLocked(ctx, receipt)
	require.ErrorIs(t, err, ErrTargetAuthorization)
}

func TestCreateControlModesPersistAndRejectUnknown(t *testing.T) {
	s := openTestStore(t)
	for _, mode := range []string{"", "STRATEGY", "MANUAL", "invalid"} {
		pauseReason := "created"
		if mode == "MANUAL" {
			pauseReason = ""
		}
		err := s.Transaction(context.Background(), func(tx *Tx) error {
			return tx.CreateLogicalAccount(LogicalAccountRecord{
				SpaceID: "space", LogicalAccountID: "account-" + mode, Name: "account-" + mode,
				ControlMode: mode, Environment: "PAPER", SettlementAsset: "USDT", PauseReason: pauseReason,
			})
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
	// 自主下单建户带暂停原因会被拒绝。
	err := s.Transaction(context.Background(), func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space", LogicalAccountID: "manual-paused", Name: "manual-paused",
			ControlMode: "MANUAL", Environment: "PAPER", SettlementAsset: "USDT", PauseReason: "should fail",
		})
	})
	require.Error(t, err)
}
