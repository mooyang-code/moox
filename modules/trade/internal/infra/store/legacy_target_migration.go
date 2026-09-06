package store

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"gorm.io/gorm"
)

const legacyStrategyTargetTableSQL = `CREATE TABLE t_logical_account_targets (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_runner_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
    c_targets_json TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_blocked_targets_json TEXT NOT NULL DEFAULT '[]',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_accepted_at INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_logical_account_id),
    UNIQUE (c_space_id, c_target_id),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    CHECK (c_command_sequence > 0),
    CHECK (c_status IN ('PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED')),
    CHECK (json_valid(c_targets_json)),
    CHECK (json_type(c_targets_json) = 'array'),
    CHECK (json_valid(c_blocked_targets_json)),
    CHECK (json_type(c_blocked_targets_json) = 'array')
)`

func validateLegacyStrategyTargetTable(db *gorm.DB) error {
	const table = "t_logical_account_targets"
	if !tableExists(db, table) {
		return nil
	}
	reference, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return err
	}
	sqlDB, err := reference.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	// Early installations omitted this optional performance index. Both exact
	// forms are recognized; an altered definition or extra index is not.
	var indexCount int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_logical_account_targets_status'").Scan(&indexCount).Error; err != nil {
		return err
	}
	legacySQL := legacyStrategyTargetTableSQL
	if indexCount != 0 {
		legacySQL += "; CREATE INDEX idx_logical_account_targets_status ON t_logical_account_targets (c_space_id, c_status, c_mtime)"
	}
	got, err := inspectTableShape(db, table)
	if err != nil {
		return err
	}
	known := false
	for _, knownSQL := range []string{legacySQL, schema.AllSQL(), strings.Replace(schema.AllSQL(), "'PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED', 'EXPIRED'", "'PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED'", 1)} {
		if err := reference.Exec("DROP TABLE IF EXISTS t_logical_account_targets").Error; err != nil {
			return err
		}
		if err := reference.Exec(knownSQL).Error; err != nil {
			return err
		}
		want, err := inspectTableShape(reference, table)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(got, want) {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("%w: unrecognized target table", ErrIncompatibleSchema)
	}
	var dependencies int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name=?", table).Scan(&dependencies).Error; err != nil {
		return err
	}
	if dependencies != 0 {
		return fmt.Errorf("%w: legacy target table has triggers", ErrIncompatibleSchema)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master AS m, pragma_foreign_key_list(m.name) AS f WHERE m.type='table' AND f."table"=?`, table).Scan(&dependencies).Error; err != nil {
		return err
	}
	if dependencies != 0 {
		return fmt.Errorf("%w: legacy target table has foreign key dependents", ErrIncompatibleSchema)
	}
	return nil
}
