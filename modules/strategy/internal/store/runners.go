package store

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
)

var ErrRunnerEnabled = errors.New("strategy runner must be disabled")

func (s *Store) CreateRunner(ctx context.Context, runner domain.StrategyRunner) error {
	if len(runner.CurrentTargetsJSON) == 0 {
		runner.CurrentTargetsJSON = []byte("[]")
	}
	runner.CommandSequence = 0
	runner.LastResultID = nil
	runner.LastSuccessAt = nil
	runner.LastError = nil
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategy_runners (
			runner_id, strategy_id, space_id, view_id, frequency, params_json,
			logical_account_id, status, current_targets_json, command_sequence,
			last_result_id, last_success_at, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		runner.ID, runner.StrategyID, runner.SpaceID, runner.ViewID, runner.Frequency,
		string(runner.ParamsJSON), stringValue(runner.LogicalAccountID), runner.Status,
		string(runner.CurrentTargetsJSON), runner.CommandSequence, stringValue(runner.LastResultID),
		timeValue(runner.LastSuccessAt), stringValue(runner.LastError),
		runner.CreatedAt.UTC().UnixMilli(), runner.UpdatedAt.UTC().UnixMilli(),
	).Error
}

func (s *Store) UpdateRunner(ctx context.Context, runner domain.StrategyRunner) error {
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_strategy_runners
		SET space_id = ?, view_id = ?, frequency = ?, params_json = ?,
		    logical_account_id = ?, updated_at = ?
		WHERE runner_id = ? AND status = ?
	`, runner.SpaceID, runner.ViewID, runner.Frequency, string(runner.ParamsJSON),
		stringValue(runner.LogicalAccountID), runner.UpdatedAt.UTC().UnixMilli(),
		runner.ID, domain.RunnerStatusDisabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return runnerMutationError(ctx, s.db, runner.ID)
	}
	return nil
}

func (s *Store) SwitchRunnerStrategy(
	ctx context.Context,
	runnerID, strategyID string,
	updatedAt time.Time,
) error {
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_strategy_runners
		SET strategy_id = ?, last_result_id = NULL, last_success_at = NULL,
		    last_error = NULL, updated_at = ?
		WHERE runner_id = ? AND status = ?
	`, strategyID, updatedAt.UTC().UnixMilli(), runnerID, domain.RunnerStatusDisabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return runnerMutationError(ctx, s.db, runnerID)
	}
	return nil
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
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_strategy_runners
		SET status = ?, updated_at = ?
		WHERE runner_id = ?
	`, status, updatedAt.UTC().UnixMilli(), runnerID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
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
