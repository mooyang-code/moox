package store

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

func (s *Store) SaveResult(ctx context.Context, result domain.StrategyResult) error {
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategy_results (
			result_id, runner_id, strategy_id, trigger_bar_time, namespace,
			input_hash, action, output_json, command_sequence, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		result.ID, result.RunnerID, result.StrategyID, result.TriggerBarTime.UTC().UnixMilli(),
		result.Namespace, result.InputHash, result.Action, string(result.OutputJSON),
		int64Value(result.CommandSequence), result.CreatedAt.UTC().UnixMilli(),
	).Error
}

func (s *Store) GetResult(ctx context.Context, resultID string) (domain.StrategyResult, error) {
	var row resultRow
	err := s.db.WithContext(ctx).Table("t_strategy_results").
		Where("result_id = ?", resultID).
		Take(&row).Error
	return row.domain(), err
}
