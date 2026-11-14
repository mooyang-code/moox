package store

import "context"

// HasEnabledTasks 报告是否存在启用的归档任务；没有时不要求有 worker 在线。
func (s *Store) HasEnabledTasks(ctx context.Context) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_tasks WHERE c_status = 'enabled'`).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
