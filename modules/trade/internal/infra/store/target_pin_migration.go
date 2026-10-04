package store

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"gorm.io/gorm"
)

const TargetPinMigrationPauseReason = "target_pin_migration_requires_new_claim"

// Current executable targets are not receipts: unknown fields, including old
// execution pins, must never silently disappear when a worker reads them.
func decodeCurrentTargets(raw string, allowPins bool) ([]InstrumentTarget, bool, error) {
	d := json.NewDecoder(strings.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('[') {
		return nil, false, fmt.Errorf("%w: target must be an array", ErrInvalidRecord)
	}
	targets := []InstrumentTarget{}
	pinned := false
	for d.More() {
		tok, err = d.Token()
		if err != nil || tok != json.Delim('{') {
			return nil, false, fmt.Errorf("%w: target must be an object", ErrInvalidRecord)
		}
		fields := map[string]string{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, false, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, false, ErrInvalidRecord
			}
			if _, duplicate := fields[name]; duplicate {
				return nil, false, fmt.Errorf("%w: duplicate target field", ErrInvalidRecord)
			}
			switch name {
			case "instrument_id", "quantity":
			case "trading_account_id", "exchange_symbol":
				if !allowPins {
					return nil, false, fmt.Errorf("%w: executable target contains a legacy pin", ErrInvalidRecord)
				}
			default:
				return nil, false, fmt.Errorf("%w: unknown target field %s", ErrInvalidRecord, name)
			}
			var value string
			if err := d.Decode(&value); err != nil || blank(value) {
				return nil, false, fmt.Errorf("%w: invalid target field %s", ErrInvalidRecord, name)
			}
			fields[name] = value
		}
		if _, err := d.Token(); err != nil {
			return nil, false, err
		}
		_, account := fields["trading_account_id"]
		_, symbol := fields["exchange_symbol"]
		if account != symbol {
			return nil, false, fmt.Errorf("%w: incomplete legacy target pin", ErrInvalidRecord)
		}
		if len(targets) > 0 && pinned != account {
			return nil, false, fmt.Errorf("%w: mixed pinned and unpinned target shapes", ErrInvalidRecord)
		}
		pinned = pinned || account
		targets = append(targets, InstrumentTarget{InstrumentID: fields["instrument_id"], Quantity: fields["quantity"]})
	}
	if _, err := d.Token(); err != nil {
		return nil, false, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, false, fmt.Errorf("%w: trailing target JSON", ErrInvalidRecord)
	}
	if _, err := encodeInstrumentTargets(targets); err != nil {
		return nil, false, err
	}
	return targets, pinned, nil
}

// Runs before Store.Open returns, hence before any execution worker can start.
// Receipts and trading facts are immutable history and are intentionally untouched.
func migratePinnedCurrentTargets(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var rows []logicalAccountTargetRow
		if err := tx.Find(&rows).Error; err != nil {
			return err
		}
		affected := []logicalAccountTargetRow{}
		for _, row := range rows {
			_, pinned, err := decodeCurrentTargets(row.TargetsJSON, true)
			if err != nil {
				return fmt.Errorf("%w: current target %s: %v", ErrIncompatibleSchema, row.TargetID, err)
			}
			if pinned {
				affected = append(affected, row)
			}
		}
		if len(affected) == 0 {
			return nil
		}
		var dependencies int64
		if err := tx.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name IN ('t_logical_account_targets', 't_logical_accounts')`).Scan(&dependencies).Error; err != nil {
			return err
		}
		if dependencies != 0 {
			return fmt.Errorf("%w: target pin migration has unrecognized triggers", ErrIncompatibleSchema)
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM sqlite_master AS m, pragma_foreign_key_list(m.name) AS f WHERE m.type = 'table' AND f."table" = 't_logical_account_targets'`).Scan(&dependencies).Error; err != nil {
			return err
		}
		if dependencies != 0 {
			return fmt.Errorf("%w: target pin migration has foreign key dependents", ErrIncompatibleSchema)
		}
		for _, row := range affected {
			result := tx.Exec(`UPDATE t_logical_accounts SET c_automation_state = 'PAUSED', c_pause_reason = ?, c_owner_runner_id = NULL, c_owner_instance_id = NULL, c_owner_session_id = NULL, c_owner_claimed_at = c_owner_claimed_at + 1, c_auth_fence = ?, c_mtime = CURRENT_TIMESTAMP WHERE c_space_id = ? AND c_logical_account_id = ?`, TargetPinMigrationPauseReason, newAuthFence(), row.SpaceID, row.LogicalAccountID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: pinned target has no logical account", ErrIncompatibleSchema)
			}
			if err := tx.Exec(`DELETE FROM t_logical_account_targets WHERE c_space_id = ? AND c_logical_account_id = ?`, row.SpaceID, row.LogicalAccountID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
