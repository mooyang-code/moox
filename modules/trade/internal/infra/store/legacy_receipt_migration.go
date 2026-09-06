package store

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"gorm.io/gorm"
)

const legacyTargetReceiptTableSQL = `CREATE TABLE t_logical_account_target_receipts (
    c_space_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_runner_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
    c_request_hash TEXT NOT NULL,
    c_signal_time INTEGER NOT NULL,
    c_weights_json TEXT NOT NULL,
    c_equity TEXT NOT NULL,
    c_equity_source_time INTEGER NOT NULL,
    c_reference_prices_json TEXT NOT NULL,
    c_quantity_targets_json TEXT NOT NULL,
    c_accepted_at INTEGER NOT NULL,
    PRIMARY KEY (c_space_id, c_target_id),
    UNIQUE (c_space_id, c_logical_account_id, c_runner_id, c_command_sequence),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    CHECK (c_command_sequence > 0),
    CHECK (json_valid(c_weights_json)),
    CHECK (json_type(c_weights_json) = 'array'),
    CHECK (json_valid(c_reference_prices_json)),
    CHECK (json_type(c_reference_prices_json) = 'object'),
    CHECK (json_valid(c_quantity_targets_json)),
    CHECK (json_type(c_quantity_targets_json) = 'array')
)`

func validateLegacyTargetReceiptTable(db *gorm.DB) error {
	const table = "t_logical_account_target_receipts"
	var tableNames []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type='table' AND name=? COLLATE NOCASE", table).Scan(&tableNames).Error; err != nil {
		return err
	}
	if len(tableNames) == 0 {
		return nil
	}
	if len(tableNames) != 1 || tableNames[0] != table {
		return fmt.Errorf("%w: unrecognized target receipt table name", ErrIncompatibleSchema)
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
	got, err := inspectTableShape(db, table)
	if err != nil {
		return err
	}
	gotSQL, err := receiptSchemaTokens(db)
	if err != nil {
		return err
	}
	var indexCount int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_target_receipts_logical' COLLATE NOCASE").Scan(&indexCount).Error; err != nil {
		return err
	}
	legacySQL := legacyTargetReceiptTableSQL
	if indexCount != 0 {
		legacySQL += "; CREATE INDEX idx_target_receipts_logical ON t_logical_account_target_receipts (c_space_id, c_logical_account_id, c_accepted_at)"
	}
	// The shipped schema retained this comment; the original migration fixture
	// omitted it. Recognize both exact historical forms without weakening SQL checks.
	commentedSQL := strings.Replace(legacySQL, "    UNIQUE (", `    -- The runner is part of the sequence namespace. command_sequence remains
    -- monotonic for a runner, while target_id is the replay/idempotency key
    -- for one accepted command.
    UNIQUE (`, 1)
	known := false
	for _, ddl := range []string{legacySQL, commentedSQL, schema.AllSQL()} {
		if err := reference.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			return err
		}
		if err := reference.Exec(ddl).Error; err != nil {
			return err
		}
		want, err := inspectTableShape(reference, table)
		if err != nil {
			return err
		}
		wantSQL, err := receiptSchemaTokens(reference)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(got, want) && reflect.DeepEqual(gotSQL, wantSQL) {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("%w: unrecognized target receipt table", ErrIncompatibleSchema)
	}
	var dependencies int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name=? COLLATE NOCASE", table).Scan(&dependencies).Error; err != nil {
		return err
	}
	if dependencies != 0 {
		return fmt.Errorf("%w: target receipt table has triggers", ErrIncompatibleSchema)
	}
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master AS m, pragma_foreign_key_list(m.name) AS f WHERE m.type='table' AND f."table"=? COLLATE NOCASE`, table).Scan(&dependencies).Error; err != nil {
		return err
	}
	if dependencies != 0 {
		return fmt.Errorf("%w: target receipt table has foreign key dependents", ErrIncompatibleSchema)
	}
	return nil
}

// Supplement the shared shape inspector without changing other migrations:
// its SQL normalization folds case and removes quotes even inside literals.
func receiptSchemaTokens(db *gorm.DB) ([][]string, error) {
	var definitions []string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE tbl_name = 't_logical_account_target_receipts' COLLATE NOCASE AND type IN ('table','index') AND sql IS NOT NULL ORDER BY type,name`).Scan(&definitions).Error; err != nil {
		return nil, err
	}
	result := make([][]string, 0, len(definitions))
	for _, definition := range definitions {
		var tokens []string
		for i := 0; i < len(definition); {
			start := i
			c := definition[i]
			if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
				i++
				continue
			}
			if strings.HasPrefix(definition[i:], "--") {
				for i < len(definition) && definition[i] != '\n' {
					i++
				}
				continue
			}
			if strings.HasPrefix(definition[i:], "/*") {
				i += 2
				for i < len(definition) && !strings.HasPrefix(definition[i:], "*/") {
					i++
				}
				if i < len(definition) {
					i += 2
				}
				continue
			}
			if c == '\'' || c == '"' || c == '`' {
				i++
				for i < len(definition) {
					if definition[i] == c {
						i++
						if i < len(definition) && definition[i] == c {
							i++
							continue
						}
						break
					}
					i++
				}
				token := definition[start:i]
				// SQLite quotes the table name after the known rebuild's RENAME.
				if strings.EqualFold(token, `"t_logical_account_target_receipts"`) {
					token = "t_logical_account_target_receipts"
				}
				tokens = append(tokens, token)
				continue
			}
			i++
			if receiptSQLWord(c) {
				for i < len(definition) && receiptSQLWord(definition[i]) {
					i++
				}
			}
			tokens = append(tokens, strings.ToLower(definition[start:i]))
		}
		result = append(result, tokens)
	}
	return result, nil
}

func receiptSQLWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}
