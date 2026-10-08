package store

import (
	"context"
	"time"
)

// DeleteResultItemsBefore 删除创建时间早于 cutoff 的结果的解释明细；结果主表永久保留。
func (s *Store) DeleteResultItemsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Exec(`
		DELETE FROM t_strategy_result_items
		WHERE c_result_id IN (SELECT c_result_id FROM t_strategy_results WHERE c_ctime < ?)
	`, cutoff.UTC())
	return result.RowsAffected, result.Error
}

// DeleteReplaysBefore 删除创建时间早于 cutoff 且已结束的回放（周期记录级联删除）。
func (s *Store) DeleteReplaysBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Exec(`
		DELETE FROM t_strategy_replays
		WHERE c_ctime < ? AND c_status IN ('done', 'failed', 'cancelled')
	`, cutoff.UTC())
	return result.RowsAffected, result.Error
}
