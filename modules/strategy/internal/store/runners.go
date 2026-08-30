package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
)

var ErrRunnerEnabled = errors.New("strategy runner must be disabled")

func (s *Store) CreateRunner(ctx context.Context, runner domain.StrategyRunner) error {
	runner.CurrentTargetsJSON = []byte("[]")
	runner.CommandSequence = 0
	runner.LastResultID = nil
	runner.LastSuccessAt = nil
	runner.LastError = nil
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategy_runners (
			runner_id, strategy_id, space_id, source_view_id, frequency,
			logical_account_id, status, current_targets_json, command_sequence,
			last_result_id, last_success_at, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		runner.ID, runner.StrategyID, runner.SpaceID, runner.SourceViewID, runner.Frequency,
		stringValue(runner.LogicalAccountID), runner.Status,
		string(runner.CurrentTargetsJSON), runner.CommandSequence, stringValue(runner.LastResultID),
		timeValue(runner.LastSuccessAt), stringValue(runner.LastError),
		runner.CreatedAt.UTC().UnixMilli(), runner.UpdatedAt.UTC().UnixMilli(),
	).Error
}

func (s *Store) UpdateRunner(ctx context.Context, runner domain.StrategyRunner) error {
	// command_sequence is a Runner-wide monotonic namespace. It must survive
	// strategy/account changes so Trade cannot mistake a new target for an old
	// replay or collide with an existing receipt.
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current runnerRow
		if err := tx.Table("t_strategy_runners").Where("runner_id = ?", runner.ID).Take(&current).Error; err != nil {
			return err
		}
		if domain.RunnerStatus(current.Status) != domain.RunnerStatusDisabled {
			return ErrRunnerEnabled
		}
		currentLogicalAccountID := ""
		if current.LogicalAccountID.Valid {
			currentLogicalAccountID = current.LogicalAccountID.String
		}
		requestedLogicalAccountID := ""
		if runner.LogicalAccountID != nil {
			requestedLogicalAccountID = *runner.LogicalAccountID
		}
		changed := current.StrategyID != runner.StrategyID ||
			currentLogicalAccountID != requestedLogicalAccountID
		if changed {
			// Results remain immutable audit records, but their not-yet-relayed
			// commands belong to the old binding and must not execute after a
			// strategy/account rebind. Remove only this runner's pending outbox
			// messages; other runners and historical results are untouched.
			if err := tx.Exec(`DELETE FROM t_strategy_outbox WHERE message_id IN (SELECT result_id FROM t_strategy_results WHERE runner_id = ?)`, runner.ID).Error; err != nil {
				return err
			}
		}
		result := tx.Exec(`
		UPDATE t_strategy_runners
		SET strategy_id = ?,
		    space_id = ?,
		    source_view_id = ?,
		    frequency = ?,
		    logical_account_id = ?,
		    current_targets_json = CASE
		        WHEN strategy_id <> ? OR COALESCE(logical_account_id, '') <> COALESCE(?, '')
		        THEN '[]' ELSE current_targets_json END,
		    last_result_id = CASE
		        WHEN strategy_id <> ? OR COALESCE(logical_account_id, '') <> COALESCE(?, '')
		        THEN NULL ELSE last_result_id END,
		    last_success_at = CASE
		        WHEN strategy_id <> ? OR COALESCE(logical_account_id, '') <> COALESCE(?, '')
		        THEN NULL ELSE last_success_at END,
		    last_error = CASE
		        WHEN strategy_id <> ? OR COALESCE(logical_account_id, '') <> COALESCE(?, '')
		        THEN NULL ELSE last_error END,
		    updated_at = ?
		WHERE runner_id = ? AND status = ?
		`, runner.StrategyID, runner.SpaceID, runner.SourceViewID, runner.Frequency,
			stringValue(runner.LogicalAccountID),
			runner.StrategyID, stringValue(runner.LogicalAccountID),
			runner.StrategyID, stringValue(runner.LogicalAccountID),
			runner.StrategyID, stringValue(runner.LogicalAccountID),
			runner.StrategyID, stringValue(runner.LogicalAccountID),
			runner.UpdatedAt.UTC().UnixMilli(), runner.ID, domain.RunnerStatusDisabled)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return runnerMutationError(ctx, tx, runner.ID)
		}
		return nil
	})
	return err
}

func (s *Store) SetRunnerStatus(
	ctx context.Context,
	runnerID string,
	status domain.RunnerStatus,
	updatedAt time.Time,
) error {
	if status != domain.RunnerStatusDisabled && status != domain.RunnerStatusEnabled {
		return errors.New("invalid strategy runner status")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current runnerRow
		if err := tx.Table("t_strategy_runners").Where("runner_id = ?", runnerID).Take(&current).Error; err != nil {
			return err
		}
		// Enabling starts a fresh active lifecycle. The Trade owner release may
		// have removed the live target; clear Strategy's cached target as well so
		// the next period is emitted as a rebalance instead of a hold that never
		// reaches Trade.
		if status == domain.RunnerStatusEnabled && domain.RunnerStatus(current.Status) == domain.RunnerStatusDisabled {
			return tx.Exec(`
				UPDATE t_strategy_runners
				SET status = ?, current_targets_json = '[]', last_result_id = NULL,
				    last_success_at = NULL, last_error = NULL, updated_at = ?
				WHERE runner_id = ?
			`, status, updatedAt.UTC().UnixMilli(), runnerID).Error
		}
		return tx.Exec(`
			UPDATE t_strategy_runners
			SET status = ?, updated_at = ?
			WHERE runner_id = ?
		`, status, updatedAt.UTC().UnixMilli(), runnerID).Error
	})
}

// ResetRunnerLifecycle clears Strategy's live snapshot and pending commands
// after Trade starts a new owner generation for an archived same-ID runner.
// Historical results remain intact; the next successful period must emit a
// fresh rebalance instead of being collapsed into a hold.
func (s *Store) ResetRunnerLifecycle(ctx context.Context, runnerID string, expectedGeneration int64, at time.Time) error {
	if s == nil || s.db == nil || runnerID == "" {
		return errors.New("runner id is required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var runner runnerRow
		if err := tx.Table("t_strategy_runners").Where("runner_id = ?", runnerID).Take(&runner).Error; err != nil {
			return err
		}
		if !runner.LastResultID.Valid {
			return nil
		}
		var result resultRow
		if err := tx.Table("t_strategy_results").Where("result_id = ?", runner.LastResultID.String).Take(&result).Error; err != nil {
			return err
		}
		var debug struct {
			OwnerGeneration int64 `json:"owner_generation"`
		}
		_ = json.Unmarshal([]byte(result.DebugInfoJSON), &debug)
		if expectedGeneration > 0 && debug.OwnerGeneration == expectedGeneration {
			// A newer V2 result was committed after the rebind; do not clear it
			// when a retry only needs to persist the archive completion marker.
			return nil
		}
		if err := tx.Exec(`
			UPDATE t_strategy_runners
			SET current_targets_json = '[]', last_result_id = NULL,
				last_success_at = NULL, last_error = NULL, updated_at = ?
			WHERE runner_id = ? AND status = ?
		`, at.UTC().UnixMilli(), runnerID, domain.RunnerStatusEnabled).Error; err != nil {
			return err
		}
		return tx.Exec(`
			DELETE FROM t_strategy_outbox
			WHERE message_id IN (
				SELECT result_id FROM t_strategy_results
				WHERE runner_id = ? AND COALESCE(json_extract(debug_info_json, '$.owner_generation'), 0) <> ?
			)
		`, runnerID, expectedGeneration).Error
	})
}

func runnerMutationError(ctx context.Context, db *gorm.DB, runnerID string) error {
	var count int64
	if err := db.WithContext(ctx).Table("t_strategy_runners").
		Where("runner_id = ?", runnerID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return ErrRunnerEnabled
}

func (s *Store) GetRunner(ctx context.Context, runnerID string) (domain.StrategyRunner, error) {
	var row runnerRow
	err := s.db.WithContext(ctx).Table("t_strategy_runners").
		Where("runner_id = ?", runnerID).
		Take(&row).Error
	return row.domain(), err
}
