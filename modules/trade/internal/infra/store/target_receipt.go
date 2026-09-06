package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type TargetReceiptRecord struct {
	SpaceID             string
	TargetID            string
	RunnerID            string
	LogicalAccountID    string
	CommandSequence     uint64
	InstanceID          string
	SessionID           string
	StrategyID          string
	BarEndTime          int64
	EffectiveAt         int64
	ValidUntil          int64
	RequestHash         string
	SignalTime          int64
	WeightsJSON         string
	Equity              string
	EquitySourceTime    int64
	ReferencePricesJSON string
	QuantityTargetsJSON string
	AcceptedAt          int64
}

type targetReceiptRow struct {
	SpaceID             string `gorm:"column:c_space_id"`
	TargetID            string `gorm:"column:c_target_id"`
	RunnerID            string `gorm:"column:c_runner_id"`
	LogicalAccountID    string `gorm:"column:c_logical_account_id"`
	CommandSequence     uint64 `gorm:"column:c_command_sequence"`
	InstanceID          string `gorm:"column:c_instance_id"`
	SessionID           string `gorm:"column:c_session_id"`
	StrategyID          string `gorm:"column:c_strategy_id"`
	BarEndTime          int64  `gorm:"column:c_bar_end_time"`
	EffectiveAt         int64  `gorm:"column:c_effective_at"`
	ValidUntil          int64  `gorm:"column:c_valid_until"`
	RequestHash         string `gorm:"column:c_request_hash"`
	SignalTime          int64  `gorm:"column:c_signal_time"`
	WeightsJSON         string `gorm:"column:c_weights_json"`
	Equity              string `gorm:"column:c_equity"`
	EquitySourceTime    int64  `gorm:"column:c_equity_source_time"`
	ReferencePricesJSON string `gorm:"column:c_reference_prices_json"`
	QuantityTargetsJSON string `gorm:"column:c_quantity_targets_json"`
	AcceptedAt          int64  `gorm:"column:c_accepted_at"`
}

func (tx *Tx) InsertTargetReceipt(record TargetReceiptRecord) error {
	newIdentity := record.InstanceID != "" || record.SessionID != "" || record.BarEndTime != 0 || record.ValidUntil != 0
	if record.SpaceID == "" || record.TargetID == "" || record.LogicalAccountID == "" || record.RequestHash == "" || record.AcceptedAt <= 0 || (!newIdentity && (record.RunnerID == "" || record.CommandSequence == 0 || record.SignalTime <= 0)) {
		return fmt.Errorf("%w: incomplete target receipt", ErrInvalidRecord)
	}
	if newIdentity && (record.InstanceID == "" || record.SessionID == "" || record.StrategyID == "" || record.BarEndTime <= 0 || record.EffectiveAt != record.BarEndTime || record.ValidUntil <= record.EffectiveAt) {
		return fmt.Errorf("%w: incomplete target receipt session contract", ErrInvalidRecord)
	}
	for name, raw := range map[string]string{"weights": record.WeightsJSON, "reference_prices": record.ReferencePricesJSON, "quantity_targets": record.QuantityTargetsJSON} {
		if raw == "" || !json.Valid([]byte(raw)) {
			return fmt.Errorf("%w: invalid target receipt %s", ErrInvalidRecord, name)
		}
	}
	var quantityTargets []InstrumentTarget
	if err := json.Unmarshal([]byte(record.QuantityTargetsJSON), &quantityTargets); err != nil {
		return fmt.Errorf("%w: invalid target receipt quantity_targets", ErrInvalidRecord)
	}
	if len(quantityTargets) > 0 && (record.Equity == "" || record.EquitySourceTime <= 0) {
		return fmt.Errorf("%w: conversion receipt requires equity snapshot", ErrInvalidRecord)
	}
	if record.CommandSequence == 0 && record.BarEndTime > 0 {
		record.CommandSequence = uint64(record.BarEndTime)
	}
	if record.SignalTime == 0 && record.BarEndTime > 0 {
		record.SignalTime = record.BarEndTime
	}
	err := tx.db.Exec(`
		INSERT INTO t_logical_account_target_receipts (
			c_space_id, c_target_id, c_runner_id, c_logical_account_id,
			c_command_sequence, c_instance_id, c_session_id, c_strategy_id,
			c_bar_end_time, c_effective_at, c_valid_until,
			c_request_hash, c_signal_time, c_weights_json,
			c_equity, c_equity_source_time, c_reference_prices_json,
			c_quantity_targets_json, c_accepted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.SpaceID, record.TargetID, record.RunnerID, record.LogicalAccountID,
		record.CommandSequence, record.InstanceID, record.SessionID, record.StrategyID,
		record.BarEndTime, record.EffectiveAt, record.ValidUntil,
		record.RequestHash, record.SignalTime, record.WeightsJSON,
		record.Equity, record.EquitySourceTime, record.ReferencePricesJSON,
		record.QuantityTargetsJSON, record.AcceptedAt).Error
	return writeError(err)
}

func (s *Store) GetTargetReceipt(ctx context.Context, spaceID, targetID string) (TargetReceiptRecord, error) {
	var row targetReceiptRow
	err := s.db.WithContext(ctx).Table("t_logical_account_target_receipts").Where("c_space_id = ? AND c_target_id = ?", spaceID, targetID).Take(&row).Error
	if err != nil {
		return TargetReceiptRecord{}, err
	}
	return row.targetReceipt(), nil
}

func (s *Store) ListTargetReceipts(ctx context.Context, spaceID, logicalAccountID string) ([]TargetReceiptRecord, error) {
	var rows []targetReceiptRow
	query := s.db.WithContext(ctx).Table("t_logical_account_target_receipts").Where("c_space_id = ? AND c_logical_account_id = ?", spaceID, logicalAccountID).Order("c_bar_end_time, c_command_sequence")
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]TargetReceiptRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.targetReceipt())
	}
	return result, nil
}

func (r targetReceiptRow) targetReceipt() TargetReceiptRecord {
	return TargetReceiptRecord{SpaceID: r.SpaceID, TargetID: r.TargetID, RunnerID: r.RunnerID, LogicalAccountID: r.LogicalAccountID, CommandSequence: r.CommandSequence, InstanceID: r.InstanceID, SessionID: r.SessionID, StrategyID: r.StrategyID, BarEndTime: r.BarEndTime, EffectiveAt: r.EffectiveAt, ValidUntil: r.ValidUntil, RequestHash: r.RequestHash, SignalTime: r.SignalTime, WeightsJSON: r.WeightsJSON, Equity: r.Equity, EquitySourceTime: r.EquitySourceTime, ReferencePricesJSON: r.ReferencePricesJSON, QuantityTargetsJSON: r.QuantityTargetsJSON, AcceptedAt: r.AcceptedAt}
}

func (r TargetReceiptRecord) AcceptedAtTime() time.Time { return time.UnixMilli(r.AcceptedAt).UTC() }

// AcceptLogicalAccountTargetWithReceipt atomically accepts the current target
// and writes its first conversion receipt. A replay with the same request hash
// is idempotent; a different payload for the same target ID is a conflict.
func (s *Store) AcceptLogicalAccountTargetWithReceipt(ctx context.Context, target LogicalAccountTargetRecord, receipt TargetReceiptRecord, ownerGenerations ...int64) (LogicalAccountTargetRecord, bool, error) {
	unlock := s.LockLogicalAccount(target.SpaceID, target.LogicalAccountID)
	defer unlock()
	return s.acceptLogicalAccountTargetWithReceipt(ctx, target, receipt, ownerGenerations...)
}

// AcceptLogicalAccountTargetWithReceiptLocked is the same atomic operation as
// AcceptLogicalAccountTargetWithReceipt, but requires the caller to already
// hold the logical-account lock. Trade uses this while converting weights so
// membership, equity and the persisted receipt observe one lifecycle.
func (s *Store) AcceptLogicalAccountTargetWithReceiptLocked(ctx context.Context, target LogicalAccountTargetRecord, receipt TargetReceiptRecord, ownerGenerations ...int64) (LogicalAccountTargetRecord, bool, error) {
	return s.acceptLogicalAccountTargetWithReceipt(ctx, target, receipt, ownerGenerations...)
}

func (s *Store) acceptLogicalAccountTargetWithReceipt(ctx context.Context, target LogicalAccountTargetRecord, receipt TargetReceiptRecord, ownerGenerations ...int64) (LogicalAccountTargetRecord, bool, error) {
	var current LogicalAccountTargetRecord
	var accepted bool
	err := s.Transaction(ctx, func(tx *Tx) error {
		account, accountErr := tx.GetLogicalAccount(target.SpaceID, target.LogicalAccountID)
		if accountErr != nil {
			return accountErr
		}
		if account.ControlMode != "STRATEGY" {
			return fmt.Errorf("%w: logical account is not strategy controlled", ErrTargetAuthorization)
		}
		// New targets are checked against the current Trade authorization and
		// validity window before receipt replay. A stale duplicate must not turn
		// an old accepted receipt into a way around session/expiry fencing.
		if target.InstanceID != "" || target.SessionID != "" || target.BarEndTime != 0 || target.ValidUntil != 0 {
			if receipt.InstanceID != target.InstanceID || receipt.SessionID != target.SessionID || receipt.StrategyID != target.StrategyID || receipt.BarEndTime != target.BarEndTime || receipt.EffectiveAt != target.EffectiveAt || receipt.ValidUntil != target.ValidUntil {
				return fmt.Errorf("%w: target and receipt session contract mismatch", ErrInvalidRecord)
			}
			if account.OwnerInstanceID != target.InstanceID || account.OwnerSessionID != target.SessionID {
				return fmt.Errorf("%w: target session authorization", ErrTargetAuthorization)
			}
			now := time.Now().UTC().UnixMilli()
			if now < target.EffectiveAt || now >= target.ValidUntil {
				return fmt.Errorf("%w: target validity window [%d,%d), now=%d", ErrTargetExpired, target.EffectiveAt, target.ValidUntil, now)
			}
		}
		var existing targetReceiptRow
		query := tx.db.Table("t_logical_account_target_receipts").Where("c_space_id = ? AND c_target_id = ?", receipt.SpaceID, receipt.TargetID).Take(&existing)
		if query.Error == nil {
			if existing.RequestHash != receipt.RequestHash {
				return fmt.Errorf("%w: target receipt request hash conflict", ErrConflict)
			}
			current = target
			accepted = false
			return nil
		}
		if !errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return query.Error
		}
		if len(ownerGenerations) > 0 {
			generation := ownerGenerations[0]
			// Legacy fixtures/accounts may not have entered an ownership
			// lifecycle yet (generation zero). Once a claim/release has advanced
			// the token, a target must carry the exact generation.
			if account.OwnerGeneration > 0 && (generation <= 0 || generation != account.OwnerGeneration) {
				return fmt.Errorf("%w: target event belongs to stale owner lifecycle", ErrConflict)
			}
		}
		var err error
		current, accepted, err = tx.AcceptLogicalAccountTarget(target)
		if err != nil || !accepted {
			return err
		}
		return tx.InsertTargetReceipt(receipt)
	})
	return current, accepted, err
}
