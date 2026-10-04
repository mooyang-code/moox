package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTargetExpiryMigrationPreservesFactsAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trade.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	oldSQL := strings.Replace(schema.AllSQL(), "'BLOCKED', 'EXPIRED'", "'BLOCKED'", 1)
	require.NoError(t, db.Exec(oldSQL).Error)
	s := &Store{db: db}
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	target, accepted, err := s.AcceptLogicalAccountTarget(ctx, validLogicalAccountTarget())
	require.NoError(t, err)
	require.True(t, accepted)
	require.NoError(t, db.Exec(`UPDATE t_logical_account_targets SET
		c_instance_id = 'instance-1', c_session_id = 'session-1', c_strategy_id = 'strategy-1',
		c_bar_end_time = 1000, c_effective_at = 1000, c_valid_until = 2000`).Error)
	target, err = s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	receipt := TargetReceiptRecord{
		SpaceID: "space-1", TargetID: target.TargetID, LogicalAccountID: "logical-1",
		RunnerID: "runner-1", CommandSequence: 1, RequestHash: "original-hash", SignalTime: 1,
		InstanceID: "instance-1", SessionID: "session-1", StrategyID: "strategy-1",
		BarEndTime: 1000, EffectiveAt: 1000, ValidUntil: 2000,
		WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: "[]", AcceptedAt: 1,
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.InsertTargetReceipt(receipt) }))
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	current, err := s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, target.TargetID, current.TargetID)
	require.Equal(t, target.InstanceID, current.InstanceID)
	require.Equal(t, target.SessionID, current.SessionID)
	require.Equal(t, target.BarEndTime, current.BarEndTime)
	require.Equal(t, target.ValidUntil, current.ValidUntil)
	require.Equal(t, target.Targets, current.Targets)
	require.Equal(t, target.AcceptedAt, current.AcceptedAt)
	current.Status = "EXPIRED"
	changed, err := s.UpdateLogicalAccountTargetState(ctx, current)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	current, err = s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "EXPIRED", current.Status)
	current.Status = "PENDING"
	changed, err = s.UpdateLogicalAccountTargetState(ctx, current)
	require.NoError(t, err)
	require.False(t, changed, "a stale execution update must not reopen an expired target")
	active, err := s.ListLogicalAccountTargets(ctx, "PENDING", "CONVERGING", "CONVERGED", "BLOCKED")
	require.NoError(t, err)
	require.Empty(t, active)
	storedReceipt, err := s.GetTargetReceipt(ctx, "space-1", target.TargetID)
	require.NoError(t, err)
	require.Equal(t, receipt, storedReceipt)
	var violations []struct{ Table string }
	require.NoError(t, s.db.Raw("PRAGMA foreign_key_check").Scan(&violations).Error)
	require.Empty(t, violations)
}

func TestTargetExpiryMigrationRejectsUnknownShape(t *testing.T) {
	for _, change := range []string{
		"ALTER TABLE t_logical_account_targets ADD COLUMN c_unknown TEXT",
		"CREATE INDEX unknown_target_index ON t_logical_account_targets(c_target_id)",
		"CREATE TRIGGER unknown_target_trigger BEFORE UPDATE ON t_logical_account_targets BEGIN SELECT 1; END",
		"CREATE TABLE unknown_target_child (space TEXT, logical TEXT, FOREIGN KEY(space, logical) REFERENCES t_logical_account_targets(c_space_id, c_logical_account_id) ON DELETE CASCADE)",
	} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trade.db")
			db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.Exec(strings.Replace(schema.AllSQL(), "'BLOCKED', 'EXPIRED'", "'BLOCKED'", 1)).Error)
			require.NoError(t, db.Exec(change).Error)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())
			_, err = Open(path)
			require.ErrorIs(t, err, ErrIncompatibleSchema)
		})
	}
}

func TestTargetExpiryMigrationFailureRollsBackAndCanRetry(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(strings.Replace(schema.AllSQL(), "'BLOCKED', 'EXPIRED'", "'BLOCKED'", 1)).Error)
	s := &Store{db: db}
	seedLogicalAccount(t, s, "runner-1")
	target, accepted, err := s.AcceptLogicalAccountTarget(context.Background(), validLogicalAccountTarget())
	require.NoError(t, err)
	require.True(t, accepted)
	before, err := inspectTableShape(db, "t_logical_account_targets")
	require.NoError(t, err)
	injected := errors.New("injected target migration failure")
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("fail_target_drop", func(tx *gorm.DB) {
		if strings.TrimSpace(tx.Statement.SQL.String()) == "DROP TABLE t_logical_account_targets" {
			tx.AddError(injected)
		}
	}))
	require.ErrorIs(t, migrateTargetExpiryStatus(db), injected)
	require.NoError(t, db.Callback().Raw().Remove("fail_target_drop"))
	after, err := inspectTableShape(db, "t_logical_account_targets")
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.False(t, tableExists(db, "t_logical_account_targets__expiry"))
	current, err := s.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, target, current)
	require.NoError(t, migrateTargetExpiryStatus(db))
	require.NoError(t, validateExistingTradeSchema(db))
}

func TestTargetExpiryCASDoesNotOverwriteNewerTarget(t *testing.T) {
	s := openTestStore(t)
	seedLogicalAccount(t, s, "runner-1")
	ctx := context.Background()
	old, accepted, err := s.AcceptLogicalAccountTarget(ctx, validLogicalAccountTarget())
	require.NoError(t, err)
	require.True(t, accepted)
	next := old
	next.TargetID = "new-target"
	next.CommandSequence++
	_, accepted, err = s.AcceptLogicalAccountTarget(ctx, next)
	require.NoError(t, err)
	require.True(t, accepted)
	old.Status = "EXPIRED"
	changed, err := s.UpdateLogicalAccountTargetState(ctx, old)
	require.NoError(t, err)
	require.False(t, changed)
	current, err := s.GetLogicalAccountTarget(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "new-target", current.TargetID)
	require.Equal(t, "PENDING", current.Status)
}
