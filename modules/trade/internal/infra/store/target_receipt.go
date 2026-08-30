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
	if record.SpaceID == "" || record.TargetID == "" || record.RunnerID == "" || record.LogicalAccountID == "" || record.CommandSequence == 0 || record.RequestHash == "" || record.SignalTime <= 0 || record.AcceptedAt <= 0 {
		return fmt.Errorf("%w: incomplete target receipt", ErrInvalidRecord)
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
	err := tx.db.Exec(`
		INSERT INTO t_logical_account_target_receipts (
			c_space_id, c_target_id, c_runner_id, c_logical_account_id,
			c_command_sequence, c_request_hash, c_signal_time, c_weights_json,
			c_equity, c_equity_source_time, c_reference_prices_json,
			c_quantity_targets_json, c_accepted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, record.SpaceID, record.TargetID, record.RunnerID, record.LogicalAccountID,
		record.CommandSequence, record.RequestHash, record.SignalTime, record.WeightsJSON,
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
	query := s.db.WithContext(ctx).Table("t_logical_account_target_receipts").Where("c_space_id = ? AND c_logical_account_id = ?", spaceID, logicalAccountID).Order("c_command_sequence")
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
	return TargetReceiptRecord{SpaceID: r.SpaceID, TargetID: r.TargetID, RunnerID: r.RunnerID, LogicalAccountID: r.LogicalAccountID, CommandSequence: r.CommandSequence, RequestHash: r.RequestHash, SignalTime: r.SignalTime, WeightsJSON: r.WeightsJSON, Equity: r.Equity, EquitySourceTime: r.EquitySourceTime, ReferencePricesJSON: r.ReferencePricesJSON, QuantityTargetsJSON: r.QuantityTargetsJSON, AcceptedAt: r.AcceptedAt}
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
			account, accountErr := tx.GetLogicalAccount(target.SpaceID, target.LogicalAccountID)
			if accountErr != nil {
				return accountErr
			}
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
