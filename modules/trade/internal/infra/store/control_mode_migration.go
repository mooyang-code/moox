package store

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"gorm.io/gorm"
)

const controlModeColumn = "c_control_mode TEXT NOT NULL DEFAULT 'STRATEGY' CHECK (c_control_mode IN ('STRATEGY', 'MANUAL'))"

type controlSchemaShape struct {
	shape  tableShape
	tokens [][]string
	mode   bool
}

type controlSchemaReferences struct {
	logical                   []controlSchemaShape
	oldAction, action         controlSchemaShape
	actionDDL, actionIndexDDL string
}

var controlReferencesOnce sync.Once
var controlReferences controlSchemaReferences
var controlReferencesErr error

func inspectControlShape(db *gorm.DB, table string) (controlSchemaShape, error) {
	shape, err := inspectTableShape(db, table)
	if err != nil {
		return controlSchemaShape{}, err
	}
	tokens, err := migrationSchemaTokens(db, table)
	return controlSchemaShape{shape: shape, tokens: tokens, mode: tableHasColumn(db, table, "c_control_mode")}, err
}

func (s controlSchemaShape) equal(other controlSchemaShape) bool {
	return reflect.DeepEqual(s.shape, other.shape) && reflect.DeepEqual(s.tokens, other.tokens)
}

func getControlReferences() (controlSchemaReferences, error) {
	controlReferencesOnce.Do(func() { controlReferences, controlReferencesErr = buildControlReferences() })
	return controlReferences, controlReferencesErr
}

func preControlModeSQL() string {
	ddl := strings.Replace(schema.AllSQL(), "    c_control_mode TEXT NOT NULL DEFAULT 'STRATEGY',\n", "", 1)
	ddl = strings.Replace(ddl, "    CHECK (c_control_mode IN ('STRATEGY', 'MANUAL')),\n", "", 1)
	return strings.Replace(ddl, "'FLATTEN', 'SUBMIT_ORDER'", "'FLATTEN'", 1)
}

func buildControlReferences() (controlSchemaReferences, error) {
	var result controlSchemaReferences
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return result, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return result, err
	}
	defer sqlDB.Close()
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		return result, err
	}
	current, err := inspectControlShape(db, "t_logical_accounts")
	if err != nil {
		return result, err
	}
	result.logical = append(result.logical, current)
	result.action, err = inspectControlShape(db, "t_operator_actions")
	if err != nil {
		return result, err
	}
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE name='t_operator_actions'").Scan(&result.actionDDL).Error; err != nil {
		return result, err
	}
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE name='idx_operator_actions_logical_account'").Scan(&result.actionIndexDDL).Error; err != nil {
		return result, err
	}
	if err := db.Exec("DROP TABLE t_logical_accounts; DROP TABLE t_operator_actions").Error; err != nil {
		return result, err
	}
	if err := db.Exec(preControlModeSQL()).Error; err != nil {
		return result, err
	}
	result.oldAction, err = inspectControlShape(db, "t_operator_actions")
	if err != nil {
		return result, err
	}
	var modernDDL string
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE name='t_logical_accounts'").Scan(&modernDDL).Error; err != nil {
		return result, err
	}

	// Generate only layouts actually emitted by the old fresh schema and its
	// atomic ALTER migrations. The three authorization columns used map order.
	ownerIndex := `CREATE UNIQUE INDEX ux_logical_account_owner_runner ON t_logical_accounts (c_space_id, c_owner_runner_id) WHERE c_owner_runner_id IS NOT NULL`
	instanceIndex := `CREATE UNIQUE INDEX ux_logical_account_owner_instance ON t_logical_accounts (c_space_id, c_owner_instance_id) WHERE c_owner_instance_id IS NOT NULL`
	add := func(ddl string, alters []string, modern bool) error {
		if err := db.Exec("DROP TABLE t_logical_accounts").Error; err != nil {
			return err
		}
		if err := db.Exec(ddl).Error; err != nil {
			return err
		}
		for _, column := range alters {
			if err := db.Exec("ALTER TABLE t_logical_accounts ADD COLUMN " + column).Error; err != nil {
				return err
			}
		}
		if err := db.Exec(ownerIndex).Error; err != nil {
			return err
		}
		if modern {
			if err := db.Exec(instanceIndex).Error; err != nil {
				return err
			}
		}
		candidate, err := inspectControlShape(db, "t_logical_accounts")
		if err != nil {
			return err
		}
		result.logical = append(result.logical, candidate)
		if modern {
			if err := db.Exec("ALTER TABLE t_logical_accounts ADD COLUMN " + controlModeColumn).Error; err != nil {
				return err
			}
			candidate, err = inspectControlShape(db, "t_logical_accounts")
			if err != nil {
				return err
			}
			result.logical = append(result.logical, candidate)
		}
		return nil
	}
	if err := add(modernDDL, nil, true); err != nil {
		return result, err
	}
	if err := add(legacyLogicalAccountTableSQL, nil, false); err != nil {
		return result, err
	}
	generationFresh := strings.Replace(legacyLogicalAccountTableSQL, "    c_owner_runner_id TEXT,", "    c_owner_runner_id TEXT,\n    c_owner_claimed_at INTEGER NOT NULL DEFAULT 0,", 1)
	auth := []string{"c_owner_instance_id TEXT", "c_owner_session_id TEXT", "c_auth_fence TEXT NOT NULL DEFAULT ''"}
	for _, appended := range []bool{false, true} {
		base, initial := generationFresh, []string{}
		if appended {
			base, initial = legacyLogicalAccountTableSQL, []string{"c_owner_claimed_at INTEGER NOT NULL DEFAULT 0"}
		}
		if err := add(base, initial, false); err != nil {
			return result, err
		}
		for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
			columns := append(append([]string{}, initial...), auth[order[0]], auth[order[1]], auth[order[2]])
			if err := add(base, columns, true); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func validateControlTable(db *gorm.DB, table string, requireMode bool) (bool, error) {
	var names []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type='table' AND name=? COLLATE NOCASE", table).Scan(&names).Error; err != nil {
		return false, err
	}
	if len(names) == 0 {
		return false, nil
	}
	if len(names) != 1 || names[0] != table {
		return false, fmt.Errorf("%w: unrecognized %s name", ErrIncompatibleSchema, table)
	}
	refs, err := getControlReferences()
	if err != nil {
		return false, err
	}
	got, err := inspectControlShape(db, table)
	if err != nil {
		return false, err
	}
	known := false
	if table == "t_logical_accounts" {
		for _, candidate := range refs.logical {
			if (!requireMode || candidate.mode) && got.equal(candidate) {
				known = true
				break
			}
		}
	} else {
		known = got.equal(refs.action) || (!requireMode && got.equal(refs.oldAction))
	}
	if !known {
		return false, fmt.Errorf("%w: unrecognized %s control schema", ErrIncompatibleSchema, table)
	}
	var triggers int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name=? COLLATE NOCASE", table).Scan(&triggers).Error; err != nil {
		return false, err
	}
	if triggers != 0 {
		return false, fmt.Errorf("%w: %s has triggers", ErrIncompatibleSchema, table)
	}
	return true, nil
}

func preflightControlModeSchema(db *gorm.DB) error {
	for _, table := range []string{"t_logical_accounts", "t_operator_actions"} {
		if _, err := validateControlTable(db, table, false); err != nil {
			return err
		}
	}
	return nil
}

func migrateControlModeSchema(db *gorm.DB) error {
	return db.Transaction(func(db *gorm.DB) error {
		if err := preflightControlModeSchema(db); err != nil {
			return err
		}
		if tableExists(db, "t_logical_accounts") && !tableHasColumn(db, "t_logical_accounts", "c_control_mode") {
			if err := db.Exec("ALTER TABLE t_logical_accounts ADD COLUMN " + controlModeColumn).Error; err != nil {
				return err
			}
		}
		if !tableExists(db, "t_operator_actions") {
			return nil
		}
		refs, err := getControlReferences()
		if err != nil {
			return err
		}
		got, err := inspectControlShape(db, "t_operator_actions")
		if err != nil {
			return err
		}
		if got.equal(refs.action) {
			return nil
		}
		var dependencies int64
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master AS m, pragma_foreign_key_list(m.name) AS f WHERE m.type='table' AND f."table"='t_operator_actions' COLLATE NOCASE`).Scan(&dependencies).Error; err != nil {
			return err
		}
		if dependencies != 0 {
			return fmt.Errorf("%w: operator action has foreign key dependents", ErrIncompatibleSchema)
		}
		ddl := strings.Replace(refs.actionDDL, "t_operator_actions", "t_operator_actions__new", 1)
		if err := db.Exec(ddl).Error; err != nil {
			return err
		}
		columns := "c_space_id,c_action_id,c_logical_account_id,c_action_type,c_reason,c_request_json,c_status,c_result_json,c_last_error,c_ctime,c_mtime"
		if err := db.Exec("INSERT INTO t_operator_actions__new (" + columns + ") SELECT " + columns + " FROM t_operator_actions").Error; err != nil {
			return err
		}
		for _, sql := range []string{"DROP TABLE t_operator_actions", "ALTER TABLE t_operator_actions__new RENAME TO t_operator_actions", refs.actionIndexDDL} {
			if err := db.Exec(sql).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
