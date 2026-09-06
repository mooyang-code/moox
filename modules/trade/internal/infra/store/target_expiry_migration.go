package store

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"gorm.io/gorm"
)

// SQLite cannot extend a CHECK in place. Only the exact preceding schema is
// eligible; unknown shapes remain untouched for strict startup validation.
func migrateTargetExpiryStatus(db *gorm.DB) error {
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
	previousSQL := strings.Replace(schema.AllSQL(),
		"'PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED', 'EXPIRED'",
		"'PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED'", 1)
	if err := reference.Exec(previousSQL).Error; err != nil {
		return err
	}
	want, err := inspectTableShape(reference, table)
	if err != nil {
		return err
	}
	got, err := inspectTableShape(db, table)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return nil
	}
	// Rebuilding must not silently drop extension triggers or cascade through
	// an unrecognized referencing table. Neither exists in the known schema.
	var dependencies int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'trigger' AND tbl_name = ?`, table).Scan(&dependencies).Error; err != nil {
		return err
	}
	if dependencies != 0 {
		return fmt.Errorf("%w: target table has unrecognized triggers", ErrIncompatibleSchema)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master AS m, pragma_foreign_key_list(m.name) AS f
		WHERE m.type = 'table' AND f."table" = ?`, table).Scan(&dependencies).Error; err != nil {
		return err
	}
	if dependencies != 0 {
		return fmt.Errorf("%w: target table has unrecognized foreign key dependents", ErrIncompatibleSchema)
	}
	var createSQL, indexSQL string
	if err := reference.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&createSQL).Error; err != nil {
		return err
	}
	if err := reference.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_logical_account_targets_status'").Scan(&indexSQL).Error; err != nil {
		return err
	}
	createSQL = strings.Replace(createSQL, table, table+"__expiry", 1)
	createSQL = strings.Replace(createSQL,
		"'PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED'",
		"'PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED', 'EXPIRED'", 1)
	return db.Transaction(func(tx *gorm.DB) error {
		for _, statement := range []string{
			createSQL,
			`INSERT INTO t_logical_account_targets__expiry SELECT * FROM t_logical_account_targets`,
			`DROP TABLE t_logical_account_targets`,
			`ALTER TABLE t_logical_account_targets__expiry RENAME TO t_logical_account_targets`,
			indexSQL,
		} {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
