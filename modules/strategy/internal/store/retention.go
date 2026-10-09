package store

import (
	"context"
	"errors"
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
			removed, err := s.deleteReplay(ctx, id)
			if err != nil {
				return deleted, err
			}
			deleted += removed
		}
	}
}

// DeleteReplaysBeyond 让每个空间只保留最近 keep 个已结束的回放：每期都记录持仓账本，一个长回放可达数十 MB，
// 只按天数清理挡不住频繁回放把数据库撑大。运行中与排队中的回放不删。
func (s *Store) DeleteReplaysBeyond(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		return 0, errors.New("每个空间保留的回放数必须大于 0")
	}
	var ids []string
	if err := s.db.WithContext(ctx).Raw(`
		SELECT c_replay_id FROM (
			SELECT c_replay_id, c_status, ROW_NUMBER() OVER (PARTITION BY c_space_id ORDER BY c_ctime DESC, c_replay_id DESC) AS c_rank
			FROM t_strategy_replays
		)
		WHERE c_rank > ? AND c_status IN ('done', 'failed', 'cancelled')
	`, keep).Scan(&ids).Error; err != nil {
		return 0, err
	}
	var deleted int64
	for _, id := range ids {
		removed, err := s.deleteReplay(ctx, id)
		if err != nil {
			return deleted, err
		}
		deleted += removed
	}
	return deleted, nil
}

// deleteReplay 先分批删除回放的周期记录，再删除任务本身，单条语句不会长时间占住唯一的数据库连接。
func (s *Store) deleteReplay(ctx context.Context, replayID string) (int64, error) {
	for {
		result := s.db.WithContext(ctx).Exec(`
			DELETE FROM t_strategy_replay_bars
			WHERE rowid IN (SELECT rowid FROM t_strategy_replay_bars WHERE c_replay_id = ? LIMIT ?)
		`, replayID, retentionBatch)
		if result.Error != nil {
			return 0, result.Error
		}
		if result.RowsAffected < retentionBatch {
			break
		}
	}
	result := s.db.WithContext(ctx).Exec(`DELETE FROM t_strategy_replays WHERE c_replay_id = ?`, replayID)
	return result.RowsAffected, result.Error
}
