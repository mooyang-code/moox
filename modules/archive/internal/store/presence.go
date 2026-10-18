package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrWorkerActive 表示 worker 仍持有活动租约，不能退役。
var ErrWorkerActive = errors.New("archive worker still holds an active lease")

// LatestWorker 返回最近一次心跳的 worker（当前单活动 worker 模型下就是正在工作或最后工作的那台）；ok 为 false 表示没有任何登记。
func (s *Store) LatestWorker(ctx context.Context) (Worker, bool, error) {
	w, err := scanWorker(s.db.QueryRowContext(ctx, `SELECT `+workerColumns+` FROM t_archive_workers ORDER BY c_last_heartbeat_at DESC, c_worker_id LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, false, nil
	}
	if err != nil {
		return Worker{}, false, err
	}
	return w, true, nil
}

// HasEnabledTasks 报告是否存在启用的归档任务；没有时不要求有 worker 在线。
func (s *Store) HasEnabledTasks(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_tasks WHERE c_status = 'enabled'`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// RetireWorker 删除一条不再使用的 worker 登记（例如换机后的旧记录）；仍持有活动租约时拒绝。
// 返回是否删除了记录。分区上的 worker_id 只是历史标注，不随之修改。
func (s *Store) RetireWorker(ctx context.Context, workerID string, now time.Time) (bool, error) {
	var retired bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		w, err := scanWorker(tx.QueryRowContext(ctx, `SELECT `+workerColumns+` FROM t_archive_workers WHERE c_worker_id = ?`, workerID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if w.Active(now) {
			return ErrWorkerActive
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM t_archive_workers WHERE c_worker_id = ?`, workerID); err != nil {
			return err
		}
		retired = true
		return nil
	})
	return retired, err
}
