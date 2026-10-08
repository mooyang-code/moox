package store

import (
	"context"
	"time"
)

// retentionBatch 是每条删除语句处理的明细行数；分批执行，避免单连接上的长事务阻塞事件处理与接口。
var retentionBatch int64 = 5000

// DeleteResultItemsBefore 删除创建时间早于 cutoff 的解释明细；结果主表永久保留。
// 明细自带 c_ctime 索引，每次只扫描新过期的行，耗时不随历史结果增长。
func (s *Store) DeleteResultItemsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		result := s.db.WithContext(ctx).Exec(`
			DELETE FROM t_strategy_result_items
			WHERE rowid IN (SELECT rowid FROM t_strategy_result_items WHERE c_ctime < ? LIMIT ?)
		`, cutoff.UTC(), retentionBatch)
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected
		if result.RowsAffected < retentionBatch {
			return total, nil
		}
	}
}

// DeleteReplaysBefore 删除创建时间早于 cutoff 且已结束的回放：逐个回放先分批删周期记录，再删任务本身，
// 每条语句的工作量有界，不会在单连接上长时间阻塞事件处理与接口。
func (s *Store) DeleteReplaysBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	var deleted int64
	for {
		var ids []string
		if err := s.db.WithContext(ctx).Raw(`
			SELECT c_replay_id FROM t_strategy_replays
			WHERE c_ctime < ? AND c_status IN ('done', 'failed', 'cancelled')
			ORDER BY c_ctime LIMIT 20
		`, cutoff.UTC()).Scan(&ids).Error; err != nil {
			return deleted, err
		}
		if len(ids) == 0 {
			return deleted, nil
		}
		for _, id := range ids {
			for {
				result := s.db.WithContext(ctx).Exec(`
					DELETE FROM t_strategy_replay_bars
					WHERE rowid IN (SELECT rowid FROM t_strategy_replay_bars WHERE c_replay_id = ? LIMIT ?)
				`, id, retentionBatch)
				if result.Error != nil {
					return deleted, result.Error
				}
				if result.RowsAffected < retentionBatch {
					break
				}
			}
			result := s.db.WithContext(ctx).Exec(`DELETE FROM t_strategy_replays WHERE c_replay_id = ?`, id)
			if result.Error != nil {
				return deleted, result.Error
			}
			deleted += result.RowsAffected
		}
	}
}
