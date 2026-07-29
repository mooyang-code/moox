package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	"gorm.io/gorm"
)

type OperatorActionRecord struct {
	SpaceID          string
	ActionID         string
	LogicalAccountID string
	ActionType       string
	Reason           string
	RequestJSON      string
	Status           string
	ResultJSON       *string
	LastError        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type operatorActionRow struct {
	SpaceID          string    `gorm:"column:c_space_id"`
	ActionID         string    `gorm:"column:c_action_id"`
	LogicalAccountID string    `gorm:"column:c_logical_account_id"`
	ActionType       string    `gorm:"column:c_action_type"`
	Reason           string    `gorm:"column:c_reason"`
	RequestJSON      string    `gorm:"column:c_request_json"`
	Status           string    `gorm:"column:c_status"`
	ResultJSON       *string   `gorm:"column:c_result_json"`
	LastError        string    `gorm:"column:c_last_error"`
	CreatedAt        time.Time `gorm:"column:c_ctime"`
	UpdatedAt        time.Time `gorm:"column:c_mtime"`
}

func (operatorActionRow) TableName() string {
	return "t_operator_actions"
}

func (s *Store) CreateOperatorAction(
	ctx context.Context,
	record OperatorActionRecord,
) (OperatorActionRecord, bool, error) {
	record, err := normalizeOperatorAction(record)
	if err != nil {
		return OperatorActionRecord{}, false, err
	}
	var created bool
	var current OperatorActionRecord
	err = s.Transaction(ctx, func(tx *Tx) error {
		var ensureErr error
		current, created, ensureErr = tx.EnsureOperatorAction(record)
		return ensureErr
	})
	if err != nil {
		return OperatorActionRecord{}, false, err
	}
	return current, created, nil
}

func (tx *Tx) CreateOperatorAction(record OperatorActionRecord) (bool, error) {
	_, created, err := tx.EnsureOperatorAction(record)
	return created, err
}

func (tx *Tx) EnsureOperatorAction(
	record OperatorActionRecord,
) (OperatorActionRecord, bool, error) {
	record, err := normalizeOperatorAction(record)
	if err != nil {
		return OperatorActionRecord{}, false, err
	}
	current, err := tx.GetOperatorAction(record.SpaceID, record.ActionID)
	if err == nil {
		if !sameOperatorRequest(current, record) {
			return OperatorActionRecord{}, false, ErrConflict
		}
		return current, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return OperatorActionRecord{}, false, err
	}
	result := tx.db.Exec(`
		INSERT INTO t_operator_actions (
			c_space_id, c_action_id, c_logical_account_id, c_action_type,
			c_reason, c_request_json, c_status, c_result_json, c_last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_action_id) DO NOTHING
	`,
		record.SpaceID, record.ActionID, record.LogicalAccountID,
		record.ActionType, record.Reason, record.RequestJSON,
		record.Status, record.ResultJSON, record.LastError,
	)
	if result.Error != nil {
		return OperatorActionRecord{}, false, writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		current, err = tx.GetOperatorAction(record.SpaceID, record.ActionID)
		if err != nil {
			return OperatorActionRecord{}, false, err
		}
		if !sameOperatorRequest(current, record) {
			return OperatorActionRecord{}, false, ErrConflict
		}
		return current, false, nil
	}
	current, err = tx.GetOperatorAction(record.SpaceID, record.ActionID)
	return current, true, err
}

func normalizeOperatorAction(
	record OperatorActionRecord,
) (OperatorActionRecord, error) {
	requestJSON, err := canonicalJSON(record.RequestJSON, "operator request")
	if err != nil {
		return OperatorActionRecord{}, err
	}
	record.RequestJSON = requestJSON
	if record.Status == "" {
		record.Status = string(operatordomain.StatusRunning)
	}
	if err := operatorActionDomain(record).Validate(); err != nil {
		return OperatorActionRecord{}, fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	if record.ResultJSON != nil {
		value, canonicalErr := canonicalJSON(*record.ResultJSON, "operator result")
		if canonicalErr != nil {
			return OperatorActionRecord{}, canonicalErr
		}
		record.ResultJSON = &value
	}
	return record, nil
}

func (s *Store) GetOperatorAction(
	ctx context.Context,
	spaceID string,
	actionID string,
) (OperatorActionRecord, error) {
	var row operatorActionRow
	err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_action_id = ?", spaceID, actionID).
		Take(&row).Error
	if err != nil {
		return OperatorActionRecord{}, err
	}
	return operatorActionRecord(row), nil
}

func (tx *Tx) GetOperatorAction(
	spaceID string,
	actionID string,
) (OperatorActionRecord, error) {
	var row operatorActionRow
	err := tx.db.
		Where("c_space_id = ? AND c_action_id = ?", spaceID, actionID).
		Take(&row).Error
	if err != nil {
		return OperatorActionRecord{}, err
	}
	return operatorActionRecord(row), nil
}

func (s *Store) ListRunningOperatorActions(
	ctx context.Context,
	spaceID string,
	logicalAccountID string,
) ([]OperatorActionRecord, error) {
	var rows []operatorActionRow
	if err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_logical_account_id = ? AND c_status = ?",
			spaceID, logicalAccountID, operatordomain.StatusRunning).
		Order("c_ctime, c_action_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]OperatorActionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, operatorActionRecord(row))
	}
	return records, nil
}

func (tx *Tx) UpdateOperatorAction(
	record OperatorActionRecord,
) error {
	if err := operatorActionDomain(record).Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	var resultJSON any
	if record.ResultJSON != nil {
		value, err := canonicalJSON(*record.ResultJSON, "operator result")
		if err != nil {
			return err
		}
		resultJSON = value
	}
	result := tx.db.Exec(`
		UPDATE t_operator_actions
		SET c_status = ?, c_result_json = ?, c_last_error = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_action_id = ?
	`,
		record.Status, resultJSON, record.LastError,
		record.SpaceID, record.ActionID,
	)
	return requireUpdated(result.Error, result.RowsAffected, "operator action")
}

func operatorActionDomain(record OperatorActionRecord) operatordomain.Action {
	return operatordomain.Action{
		SpaceID: record.SpaceID, ID: record.ActionID,
		LogicalAccountID: record.LogicalAccountID,
		Type:             operatordomain.ActionType(record.ActionType),
		Reason:           record.Reason, RequestJSON: record.RequestJSON,
		Status:     operatordomain.Status(record.Status),
		ResultJSON: record.ResultJSON, LastError: record.LastError,
	}
}

func operatorActionRecord(row operatorActionRow) OperatorActionRecord {
	return OperatorActionRecord{
		SpaceID: row.SpaceID, ActionID: row.ActionID,
		LogicalAccountID: row.LogicalAccountID,
		ActionType:       row.ActionType, Reason: row.Reason,
		RequestJSON: row.RequestJSON, Status: row.Status,
		ResultJSON: row.ResultJSON, LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func sameOperatorRequest(left OperatorActionRecord, right OperatorActionRecord) bool {
	return left.SpaceID == right.SpaceID &&
		left.ActionID == right.ActionID &&
		left.LogicalAccountID == right.LogicalAccountID &&
		left.ActionType == right.ActionType &&
		left.Reason == right.Reason &&
		left.RequestJSON == right.RequestJSON
}

func canonicalJSON(raw string, label string) (string, error) {
	if !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("%w: invalid %s JSON", ErrInvalidRecord, label)
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("%w: decode %s JSON: %v", ErrInvalidRecord, label, err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: encode %s JSON: %v", ErrInvalidRecord, label, err)
	}
	return string(data), nil
}
