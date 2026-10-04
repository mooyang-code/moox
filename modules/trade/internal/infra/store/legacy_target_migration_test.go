package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func legacyTargetDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec(legacyLogicalAccountTableSQL).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX ux_logical_account_owner_runner ON t_logical_accounts (c_space_id,c_owner_runner_id) WHERE c_owner_runner_id IS NOT NULL`).Error)
	require.NoError(t, db.Exec(legacyStrategyTargetTableSQL).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_logical_accounts (c_space_id,c_logical_account_id,c_name,c_execution_mode,c_market_type,c_settlement_asset,c_automation_state,c_pause_reason) VALUES ('s','a','a','PAPER','SPOT','USDT','PAUSED','test')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_logical_account_targets (c_space_id,c_logical_account_id,c_target_id,c_runner_id,c_command_sequence,c_targets_json,c_status,c_accepted_at) VALUES ('s','a','target','runner',1,'[]','PENDING',123)`).Error)
	return db, path
}

func migrationSchemaSnapshot(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var result []string
	require.NoError(t, db.Raw(`SELECT type || ':' || name || ':' || COALESCE(sql,'') FROM sqlite_master ORDER BY type,name`).Scan(&result).Error)
	return result
}

func TestLegacyTargetMigrationRejectsUnknownShapeWithoutMutation(t *testing.T) {
	for name, extension := range map[string]string{
		"column":              `ALTER TABLE t_logical_account_targets ADD COLUMN c_extension TEXT DEFAULT 'retained'`,
		"instance-only":       `ALTER TABLE t_logical_account_targets ADD COLUMN c_instance_id TEXT NOT NULL DEFAULT ''`,
		"index":               `CREATE INDEX custom_target_index ON t_logical_account_targets(c_runner_id)`,
		"altered known index": `CREATE INDEX idx_logical_account_targets_status ON t_logical_account_targets(c_runner_id)`,
		"trigger":             `CREATE TRIGGER custom_target_trigger AFTER UPDATE ON t_logical_account_targets BEGIN SELECT 1; END`,
		"foreign key":         `CREATE TABLE custom_target_children (c_space_id TEXT,c_target_id TEXT,FOREIGN KEY(c_space_id,c_target_id) REFERENCES t_logical_account_targets(c_space_id,c_target_id) ON DELETE CASCADE)`,
	} {
		t.Run(name, func(t *testing.T) {
			db, path := legacyTargetDB(t)
			require.NoError(t, db.Exec(extension).Error)
			if name == "foreign key" {
				require.NoError(t, db.Exec(`INSERT INTO custom_target_children VALUES ('s','target')`).Error)
			}
			before := migrationSchemaSnapshot(t, db)
			s, err := Open(path)
			if s != nil {
				_ = s.Close()
			}
			require.ErrorIs(t, err, ErrIncompatibleSchema)
			require.Equal(t, before, migrationSchemaSnapshot(t, db))
			var accepted int64
			require.NoError(t, db.Raw(`SELECT c_accepted_at FROM t_logical_account_targets WHERE c_target_id='target'`).Scan(&accepted).Error)
			require.Equal(t, int64(123), accepted)
			if name == "column" {
				var value string
				require.NoError(t, db.Raw(`SELECT c_extension FROM t_logical_account_targets`).Scan(&value).Error)
				require.Equal(t, "retained", value)
			}
			if name == "foreign key" {
				var count int64
				require.NoError(t, db.Raw(`SELECT COUNT(*) FROM custom_target_children`).Scan(&count).Error)
				require.Equal(t, int64(1), count)
			}
		})
	}
}

func TestLegacyTargetMigrationDDLFailureRollsBackDropAndCopy(t *testing.T) {
	db, _ := legacyTargetDB(t)
	before := migrationSchemaSnapshot(t, db)
	injected := errors.New("injected rename failure")
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("reject_target_rename", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "ALTER TABLE t_logical_account_targets__new RENAME") {
			tx.AddError(injected)
		}
	}))
	err := rebuildLegacyStrategyTargetTable(db)
	require.ErrorIs(t, err, injected)
	require.NoError(t, db.Callback().Raw().Remove("reject_target_rename"))
	require.Equal(t, before, migrationSchemaSnapshot(t, db))
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM t_logical_account_targets WHERE c_target_id='target' AND c_accepted_at=123`).Scan(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestLegacyTargetMigrationAcceptsKnownIndexAndPreservesFacts(t *testing.T) {
	db, _ := legacyTargetDB(t)
	require.NoError(t, db.Exec(`CREATE INDEX idx_logical_account_targets_status ON t_logical_account_targets (c_space_id, c_status, c_mtime)`).Error)
	require.NoError(t, rebuildLegacyStrategyTargetTable(db))
	require.True(t, tableHasColumn(db, "t_logical_account_targets", "c_instance_id"))
	var count int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM t_logical_account_targets WHERE c_target_id='target' AND c_accepted_at=123 AND c_instance_id=''`).Scan(&count).Error)
	require.Equal(t, int64(1), count)
	require.NoError(t, db.Exec(`UPDATE t_logical_account_targets SET c_status='EXPIRED'`).Error)
}

func TestLegacyTargetMigrationRejectsMissingModernColumnWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, db.Exec(schema.AllSQL()).Error)
	// SQLite updates the table CHECK when renaming; the resulting table is
	// usable but does not satisfy any recognized target contract.
	require.NoError(t, db.Exec(`ALTER TABLE t_logical_account_targets RENAME COLUMN c_strategy_id TO c_extension_strategy_id`).Error)
	before := migrationSchemaSnapshot(t, db)
	s, err := Open(path)
	if s != nil {
		_ = s.Close()
	}
	require.ErrorIs(t, err, ErrIncompatibleSchema)
	require.Equal(t, before, migrationSchemaSnapshot(t, db))
	require.False(t, tableHasColumn(db, "t_logical_account_targets", "c_strategy_id"))
}
