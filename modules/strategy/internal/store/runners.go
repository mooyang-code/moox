package store

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) CreateRunner(ctx context.Context, runner domain.StrategyRunner) error {
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

func (s *Store) GetRunner(ctx context.Context, runnerID string) (domain.StrategyRunner, error) {
	var row runnerRow
	err := s.db.WithContext(ctx).Table("t_strategy_runners").
		Where("runner_id = ?", runnerID).
		Take(&row).Error
	return row.domain(), err
}
