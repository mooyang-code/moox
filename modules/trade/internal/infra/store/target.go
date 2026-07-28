package store

import (
	"context"
	"encoding/json"
	"fmt"

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
}

type targetExecutionRow struct {
	SpaceID            string `gorm:"column:c_space_id"`
	ExecutionID        string `gorm:"column:c_execution_id"`
	EventID            string `gorm:"column:c_event_id"`
	StrategyRunID      string `gorm:"column:c_strategy_run_id"`
	ExecutionBindingID string `gorm:"column:c_execution_binding_id"`
	ExchangeAccountID  string `gorm:"column:c_exchange_account_id"`
	CommandSequence    uint64 `gorm:"column:c_command_sequence"`
	NotAfter           int64  `gorm:"column:c_not_after"`
	DataRevision       string `gorm:"column:c_data_revision"`
	TargetsJSON        string `gorm:"column:c_targets_json"`
	Status             string `gorm:"column:c_status"`
	Progress           string `gorm:"column:c_progress"`
	ResidualQuantity   string `gorm:"column:c_residual_quantity"`
	LastError          string `gorm:"column:c_last_error"`
}

func (targetExecutionRow) TableName() string {
	return "t_target_executions"
}

func (s *Store) AcceptTarget(
	ctx context.Context,
	record TargetExecutionRecord,
) (bool, error) {
	var accepted bool
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		accepted, err = tx.AcceptTarget(record)
		return err
	})
	return accepted, err
}

func (tx *Tx) AcceptTarget(record TargetExecutionRecord) (bool, error) {
	targetsJSON, err := encodeTargets(record.Targets)
	if err != nil {
		return false, err
	}
	if record.SpaceID == "" || record.ExecutionID == "" || record.EventID == "" ||
		record.ExecutionBindingID == "" || record.ExchangeAccountID == "" ||
		record.CommandSequence == 0 || record.Status == "" {
		return false, fmt.Errorf("%w: incomplete target execution", ErrInvalidRecord)
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
	`,
		record.SpaceID, record.ExecutionID, record.EventID, record.StrategyRunID,
		record.ExecutionBindingID, record.ExchangeAccountID, record.CommandSequence,
		record.NotAfter, record.DataRevision, targetsJSON, record.Status, record.Progress,
		defaultDecimal(record.ResidualQuantity), record.LastError,
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
	}, nil
}

func encodeTargets(targets []TargetPosition) (string, error) {
	if len(targets) == 0 {
		return "", fmt.Errorf("%w: target positions are required", ErrInvalidRecord)
	}
	canonical := make([]TargetPosition, len(targets))
	copy(canonical, targets)
	for i := range canonical {
		target := &canonical[i]
		if target.Symbol == "" {
			return "", fmt.Errorf("%w: empty target symbol", ErrInvalidRecord)
		}
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
