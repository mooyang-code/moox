package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type TargetPosition struct {
	InstrumentID   string `json:"instrument_id"`
	Symbol         string `json:"symbol"`
	TargetQuantity string `json:"target_quantity"`
}

type TargetExecutionRecord struct {
	SpaceID            string
	ExecutionID        string
	EventID            string
	StrategyRunID      string
	ExecutionBindingID string
	ExchangeAccountID  string
	CommandSequence    uint64
	NotAfter           int64
	DataRevision       string
	Targets            []TargetPosition
	Status             string
	Progress           string
	ResidualQuantity   string
	LastError          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type targetExecutionRow struct {
	SpaceID            string    `gorm:"column:c_space_id"`
	ExecutionID        string    `gorm:"column:c_execution_id"`
	EventID            string    `gorm:"column:c_event_id"`
	StrategyRunID      string    `gorm:"column:c_strategy_run_id"`
	ExecutionBindingID string    `gorm:"column:c_execution_binding_id"`
	ExchangeAccountID  string    `gorm:"column:c_exchange_account_id"`
	CommandSequence    uint64    `gorm:"column:c_command_sequence"`
	NotAfter           int64     `gorm:"column:c_not_after"`
	DataRevision       string    `gorm:"column:c_data_revision"`
	TargetsJSON        string    `gorm:"column:c_targets_json"`
	Status             string    `gorm:"column:c_status"`
	Progress           string    `gorm:"column:c_progress"`
	ResidualQuantity   string    `gorm:"column:c_residual_quantity"`
	LastError          string    `gorm:"column:c_last_error"`
	CreatedAt          time.Time `gorm:"column:c_ctime"`
	UpdatedAt          time.Time `gorm:"column:c_mtime"`
}

func (targetExecutionRow) TableName() string {
	return "t_target_executions"
}

func (s *Store) AcceptTarget(
	ctx context.Context,
	record TargetExecutionRecord,
) (bool, error) {
	unlock := s.LockTargetBinding(record.SpaceID, record.ExecutionBindingID)
	defer unlock()
	var accepted bool
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		accepted, err = tx.AcceptTarget(record)
		return err
	})
	return accepted, err
}

func (tx *Tx) AcceptTarget(record TargetExecutionRecord) (bool, error) {
	if blank(record.SpaceID) || blank(record.ExecutionID) || blank(record.EventID) ||
		record.EventID != record.ExecutionID || blank(record.StrategyRunID) ||
		blank(record.ExecutionBindingID) ||
		blank(record.ExchangeAccountID) || blank(record.DataRevision) ||
		record.CommandSequence == 0 || record.CommandSequence > math.MaxInt64 ||
		record.NotAfter <= time.Now().UnixMilli() ||
		!validTargetExecutionStatus(record.Status) {
		return false, fmt.Errorf("%w: incomplete target execution", ErrInvalidRecord)
	}
	targetsJSON, err := encodeTargets(record.Targets)
	if err != nil {
		return false, err
	}
	record.ResidualQuantity, err = canonicalDefaultZero(
		record.ResidualQuantity,
		"target residual quantity",
		decimalSigned,
	)
	if err != nil {
		return false, err
	}

	var currentBinding struct {
		ExchangeAccountID string `gorm:"column:c_exchange_account_id"`
	}
	bindingResult := tx.db.Raw(`
		SELECT c_exchange_account_id
		FROM t_target_executions
		WHERE c_space_id = ? AND c_execution_binding_id = ?
	`, record.SpaceID, record.ExecutionBindingID).Scan(&currentBinding)
	if bindingResult.Error != nil {
		return false, bindingResult.Error
	}
	if bindingResult.RowsAffected == 1 &&
		currentBinding.ExchangeAccountID != record.ExchangeAccountID {
		return false, fmt.Errorf("%w: target binding Exchange account", ErrConflict)
	}

	var duplicateEvent int64
	if err := tx.db.Raw(`
		SELECT COUNT(*)
		FROM t_target_executions
		WHERE c_space_id = ? AND c_event_id = ?
	`, record.SpaceID, record.EventID).Scan(&duplicateEvent).Error; err != nil {
		return false, err
	}
	if duplicateEvent != 0 {
		return false, nil
	}

	result := tx.db.Exec(`
		INSERT INTO t_target_executions (
			c_space_id, c_execution_id, c_event_id, c_strategy_run_id,
			c_execution_binding_id, c_exchange_account_id, c_command_sequence,
			c_not_after, c_data_revision, c_targets_json, c_status, c_progress,
			c_residual_quantity, c_last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_execution_binding_id) DO UPDATE SET
			c_execution_id = excluded.c_execution_id,
			c_event_id = excluded.c_event_id,
			c_strategy_run_id = excluded.c_strategy_run_id,
			c_exchange_account_id = excluded.c_exchange_account_id,
			c_command_sequence = excluded.c_command_sequence,
			c_not_after = excluded.c_not_after,
			c_data_revision = excluded.c_data_revision,
			c_targets_json = excluded.c_targets_json,
			c_status = excluded.c_status,
			c_progress = excluded.c_progress,
			c_residual_quantity = excluded.c_residual_quantity,
			c_last_error = excluded.c_last_error,
			c_mtime = CURRENT_TIMESTAMP
		WHERE excluded.c_command_sequence > t_target_executions.c_command_sequence
			AND excluded.c_exchange_account_id =
				t_target_executions.c_exchange_account_id
	`,
		record.SpaceID, record.ExecutionID, record.EventID, record.StrategyRunID,
		record.ExecutionBindingID, record.ExchangeAccountID, record.CommandSequence,
		record.NotAfter, record.DataRevision, targetsJSON, record.Status, record.Progress,
		record.ResidualQuantity, record.LastError,
	)
	if result.Error != nil {
		return false, writeError(result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (s *Store) GetTargetExecutionByBinding(
	ctx context.Context,
	spaceID string,
	executionBindingID string,
) (TargetExecutionRecord, error) {
	var row targetExecutionRow
	err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_execution_binding_id = ?", spaceID, executionBindingID).
		Take(&row).Error
	if err != nil {
		return TargetExecutionRecord{}, err
	}
	var targets []TargetPosition
	if err := json.Unmarshal([]byte(row.TargetsJSON), &targets); err != nil {
		return TargetExecutionRecord{}, fmt.Errorf("%w: target JSON: %v", ErrInvalidRecord, err)
	}
	return TargetExecutionRecord{
		SpaceID: row.SpaceID, ExecutionID: row.ExecutionID, EventID: row.EventID,
		StrategyRunID: row.StrategyRunID, ExecutionBindingID: row.ExecutionBindingID,
		ExchangeAccountID: row.ExchangeAccountID, CommandSequence: row.CommandSequence,
		NotAfter: row.NotAfter, DataRevision: row.DataRevision, Targets: targets,
		Status: row.Status, Progress: row.Progress,
		ResidualQuantity: row.ResidualQuantity, LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func (s *Store) GetTargetExecution(
	ctx context.Context,
	spaceID string,
	executionID string,
) (TargetExecutionRecord, error) {
	var row targetExecutionRow
	err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_execution_id = ?", spaceID, executionID).
		Take(&row).Error
	if err != nil {
		return TargetExecutionRecord{}, err
	}
	var targets []TargetPosition
	if err := json.Unmarshal([]byte(row.TargetsJSON), &targets); err != nil {
		return TargetExecutionRecord{}, fmt.Errorf("%w: target JSON: %v", ErrInvalidRecord, err)
	}
	return targetExecutionRecordFromRow(row, targets), nil
}

func (s *Store) ListTargetExecutions(
	ctx context.Context,
	statuses ...string,
) ([]TargetExecutionRecord, error) {
	query := s.db.WithContext(ctx).Table("t_target_executions")
	if len(statuses) > 0 {
		query = query.Where("c_status IN ?", statuses)
	}
	var rows []targetExecutionRow
	if err := query.Order("c_space_id, c_execution_binding_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]TargetExecutionRecord, 0, len(rows))
	for _, row := range rows {
		var targets []TargetPosition
		if err := json.Unmarshal([]byte(row.TargetsJSON), &targets); err != nil {
			return nil, fmt.Errorf("%w: target JSON: %v", ErrInvalidRecord, err)
		}
		records = append(records, targetExecutionRecordFromRow(row, targets))
	}
	return records, nil
}

type TargetExecutionQuery struct {
	ExchangeAccountID  string
	ExecutionBindingID string
	Status             string
	Offset             int
	Limit              int
}

func (s *Store) QueryTargetExecutions(
	ctx context.Context,
	spaceID string,
	query TargetExecutionQuery,
) ([]TargetExecutionRecord, int64, error) {
	db := s.db.WithContext(ctx).Table("t_target_executions").
		Where("c_space_id = ?", spaceID)
	if query.ExchangeAccountID != "" {
		db = db.Where("c_exchange_account_id = ?", query.ExchangeAccountID)
	}
	if query.ExecutionBindingID != "" {
		db = db.Where("c_execution_binding_id = ?", query.ExecutionBindingID)
	}
	if query.Status != "" {
		db = db.Where("c_status = ?", query.Status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []targetExecutionRow
	if err := db.Order("c_mtime DESC, c_execution_id DESC").
		Offset(query.Offset).Limit(query.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	records := make([]TargetExecutionRecord, 0, len(rows))
	for _, row := range rows {
		var targets []TargetPosition
		if err := json.Unmarshal([]byte(row.TargetsJSON), &targets); err != nil {
			return nil, 0, fmt.Errorf("%w: target JSON: %v", ErrInvalidRecord, err)
		}
		records = append(records, targetExecutionRecordFromRow(row, targets))
	}
	return records, total, nil
}

func (s *Store) UpdateTargetExecutionState(
	ctx context.Context,
	record TargetExecutionRecord,
) (bool, error) {
	if blank(record.SpaceID) || blank(record.ExecutionID) ||
		blank(record.ExecutionBindingID) || record.CommandSequence == 0 ||
		!validTargetExecutionStatus(record.Status) {
		return false, fmt.Errorf("%w: invalid target execution update", ErrInvalidRecord)
	}
	residual, err := canonicalDefaultZero(
		record.ResidualQuantity,
		"target residual quantity",
		decimalSigned,
	)
	if err != nil {
		return false, err
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_target_executions
		SET c_status = ?, c_progress = ?, c_residual_quantity = ?,
			c_last_error = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_execution_binding_id = ?
			AND c_execution_id = ? AND c_command_sequence = ?
	`,
		record.Status, record.Progress, residual, record.LastError,
		record.SpaceID, record.ExecutionBindingID,
		record.ExecutionID, record.CommandSequence,
	)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func targetExecutionRecordFromRow(
	row targetExecutionRow,
	targets []TargetPosition,
) TargetExecutionRecord {
	return TargetExecutionRecord{
		SpaceID: row.SpaceID, ExecutionID: row.ExecutionID, EventID: row.EventID,
		StrategyRunID: row.StrategyRunID, ExecutionBindingID: row.ExecutionBindingID,
		ExchangeAccountID: row.ExchangeAccountID, CommandSequence: row.CommandSequence,
		NotAfter: row.NotAfter, DataRevision: row.DataRevision, Targets: targets,
		Status: row.Status, Progress: row.Progress,
		ResidualQuantity: row.ResidualQuantity, LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func validTargetExecutionStatus(status string) bool {
	switch status {
	case "RUNNING", "COMPLETED", "EXPIRED", "FAILED", "PAUSED":
		return true
	default:
		return false
	}
}

func encodeTargets(targets []TargetPosition) (string, error) {
	if len(targets) == 0 {
		return "", fmt.Errorf("%w: target positions are required", ErrInvalidRecord)
	}
	canonical := make([]TargetPosition, len(targets))
	copy(canonical, targets)
	symbols := make(map[string]struct{}, len(canonical))
	for i := range canonical {
		target := &canonical[i]
		if blank(target.InstrumentID) || blank(target.Symbol) {
			return "", fmt.Errorf("%w: incomplete target identity", ErrInvalidRecord)
		}
		if _, duplicate := symbols[target.Symbol]; duplicate {
			return "", fmt.Errorf("%w: duplicate target symbol %s", ErrInvalidRecord, target.Symbol)
		}
		symbols[target.Symbol] = struct{}{}
		quantity, err := shared.ParseDecimal(target.TargetQuantity)
		if err != nil {
			return "", fmt.Errorf("%w: target quantity", ErrInvalidRecord)
		}
		target.TargetQuantity = quantity.String()
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: encode targets: %v", ErrInvalidRecord, err)
	}
	return string(data), nil
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
