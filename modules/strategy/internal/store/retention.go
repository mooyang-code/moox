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

// DeleteReplaysBefore 删除创建时间早于 cutoff 且已结束的回放（周期记录级联删除）。
func (s *Store) DeleteReplaysBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Exec(`
		DELETE FROM t_strategy_replays
		WHERE c_ctime < ? AND c_status IN ('done', 'failed', 'cancelled')
	`, cutoff.UTC())
	return result.RowsAffected, result.Error
}
