package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
