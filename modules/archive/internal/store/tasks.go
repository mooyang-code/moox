package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Task 是一个归档任务。
type Task struct {
	// ID 是自增主键，删除后不复用，用作任务实例标识。
	ID        int64
	SpaceID   string
	DatasetID string
	// Purpose 是用途（collect、factor、data），DirName 是空间与用途内唯一的目录名；分区文件在 <space>/<purpose>/<dir_name>/ 下，建好后不改。
	Purpose string
	DirName string
	Freq    string
	// BackfillFrom 是归档起点：创建时确定，之后不改；暂存区没有分区时整段重读从这里读起。
	BackfillFrom time.Time
	Status       string
	// SourceMissing 为真表示源数据集在 Storage 中不存在：不改 Status，停止下发给 worker 跟随，数据集重现时由维护刷新自动清除。
	SourceMissing bool
	// LastFollowAt 是最近一次跟随（或整段重读）尝试的开始时间；LastSuccessAt 是最近一次成功的跟随或整段重读的开始时间。
	LastFollowAt      time.Time
	LastSuccessAt     time.Time
	LastFollowError   string
	LastFollowMessage string
	// RescanFrom、RescanUntil 是整段重读进度（data_time）；两者都非零表示整段重读进行中。
	RescanFrom  time.Time
	RescanUntil time.Time
	FinalUntil  time.Time
	// Unrecoverable 是按任务累积的无法补齐区间（超出线上保留期）。
	Unrecoverable []Unrecoverable
	// BlockedReason 是分区登记被拒的原因（行数或标的数倒退），下一次被接受的带分区上报清空。
	BlockedReason string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// IsRescanning 报告任务当前是否正在整段重读中。
func (t Task) IsRescanning() bool {
	return !t.RescanFrom.IsZero() && !t.RescanUntil.IsZero()
}

const taskColumns = `c_id, c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq,
c_backfill_from, c_status, c_source_missing, c_last_follow_at, c_last_success_at, c_last_follow_error, c_last_follow_message,
c_rescan_from, c_rescan_until, c_final_until, c_unrecoverable_json, c_blocked_reason, c_ctime, c_mtime`

type rowScanner interface{ Scan(dest ...any) error }

func scanTask(row rowScanner) (Task, error) {
	var t Task
	var backfill, lastFollow, lastSuccess, rescanFrom, rescanUntil, finalUntil, created, updated timeValue
	var sourceMissing int
	var unrecoverable string
	err := row.Scan(&t.ID, &t.SpaceID, &t.DatasetID, &t.Purpose, &t.DirName, &t.Freq,
		&backfill, &t.Status, &sourceMissing, &lastFollow, &lastSuccess, &t.LastFollowError, &t.LastFollowMessage,
		&rescanFrom, &rescanUntil, &finalUntil, &unrecoverable, &t.BlockedReason, &created, &updated)
	if err != nil {
		return Task{}, err
	}
	if unrecoverable != "" && unrecoverable != "[]" {
		_ = json.Unmarshal([]byte(unrecoverable), &t.Unrecoverable)
	}
	t.SourceMissing = sourceMissing != 0
	t.BackfillFrom = backfill.Time
	t.LastFollowAt, t.LastSuccessAt = lastFollow.Time, lastSuccess.Time
	t.RescanFrom, t.RescanUntil = rescanFrom.Time, rescanUntil.Time
	t.FinalUntil, t.CreatedAt, t.UpdatedAt = finalUntil.Time, created.Time, updated.Time
	return t, nil
}

// CreateTask 创建任务；BackfillFrom 必须非零，启用的任务由 worker 本地循环跟随。
// 数据集已有任务返回 ErrConflict；目录名已被同空间同用途的其他任务使用返回 *DirNameTakenError（检查与插入在同一事务里）。
func (s *Store) CreateTask(ctx context.Context, task Task) (Task, error) {
	if task.BackfillFrom.IsZero() {
		return Task{}, errors.New("归档任务必须有回补起点")
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var holder string
		err := tx.QueryRowContext(ctx, queryTaskByDir, task.SpaceID, task.Purpose, task.DirName).Scan(&holder)
		switch {
		case err == nil && holder != task.DatasetID:
			return &DirNameTakenError{DirName: task.DirName, DatasetID: holder}
		case err == nil:
			return ErrConflict
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO t_archive_tasks (c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq, c_backfill_from, c_status)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			task.SpaceID, task.DatasetID, task.Purpose, task.DirName, task.Freq, millisOf(task.BackfillFrom), task.Status)
		if err != nil && isUnique(err) {
			// 目录名在上面已查过，这里撞上的只能是数据集已有任务。
			return ErrConflict
		}
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, task.SpaceID, task.DatasetID)
}

// DirNameHolder 返回空间内某个用途下使用 dirName 的任务的数据集 ID；没有任务使用时 ok 为假。
func (s *Store) DirNameHolder(ctx context.Context, spaceID, purpose, dirName string) (datasetID string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, queryTaskByDir, spaceID, purpose, dirName).Scan(&datasetID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return datasetID, err == nil, err
}

// GetTask 读取一个任务。
func (s *Store) GetTask(ctx context.Context, spaceID, datasetID string) (Task, error) {
	task, err := scanTask(s.db.QueryRowContext(ctx, queryTasks1, spaceID, datasetID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// ListTasks 按数据集排序分页列出空间的任务；spaceID 为空表示全部空间。
func (s *Store) ListTasks(ctx context.Context, spaceID string, limit, offset int) ([]Task, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_tasks WHERE (? = '' OR c_space_id = ?)`, spaceID, spaceID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, queryTasks2, spaceID, spaceID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, task)
	}
	return out, total, rows.Err()
}

// ListFollowableTasks 返回当前启用、源数据集未缺失且没有排队中或运行中运行（manual、range、import）的任务，供 worker 本地循环跟随。
func (s *Store) ListFollowableTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, queryTasksFollowable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// TaskUpdate 是 UpdateTask 的修改内容（完整的新值）。
type TaskUpdate struct {
	Status string
}

// UpdateTask 修改任务状态；停用时同时取消排队中的运行，重新启用后从原位点继续跟随。
func (s *Store) UpdateTask(ctx context.Context, spaceID, datasetID string, update TaskUpdate, now time.Time) (Task, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_status = ? WHERE c_space_id = ? AND c_dataset_id = ?`,
			update.Status, spaceID, datasetID)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return ErrNotFound
		}
		if update.Status == StatusDisabled {
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'cancelled', c_finished_at = ?, c_message = '任务已停用'
WHERE c_space_id = ? AND c_dataset_id = ? AND c_status = 'queued'`, millisOf(now), spaceID, datasetID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return s.GetTask(ctx, spaceID, datasetID)
}

// DeleteTask 删除任务；已有分区登记的任务不能删除。返回实际被删除的任务 ID（同一事务内读取），用作删除通知的实例标识。
func (s *Store) DeleteTask(ctx context.Context, spaceID, datasetID string) (int64, error) {
	var deleted int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var partitions int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_partitions WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID).Scan(&partitions); err != nil {
			return err
		}
		if partitions > 0 {
			return ErrHasPartitions
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_runs WHERE c_space_id = ? AND c_dataset_id = ? AND c_status = 'running'`, spaceID, datasetID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrConflict
		}
		if err := tx.QueryRowContext(ctx, `SELECT c_id FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID).Scan(&deleted); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM t_archive_tasks WHERE c_id = ?`, deleted)
		return err
	})
	return deleted, err
}

// SetTaskSourceMissing 更新任务的“源数据集不存在”标记（不修改 c_status，也不覆盖 c_blocked_reason）。
func (s *Store) SetTaskSourceMissing(ctx context.Context, spaceID, datasetID string, missing bool) error {
	val := 0
	if missing {
		val = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE t_archive_tasks SET c_source_missing = ?
WHERE c_space_id = ? AND c_dataset_id = ? AND c_source_missing != ?`,
		val, spaceID, datasetID, val)
	return err
}

// ClearUnrecoverable 清除任务累积的无法补齐区间：start 与 end 都为零值时清除全部；
// 否则删掉整段落在 [start, end) 里的（起点不早于 start、终点不晚于 end），返回删掉的条目。
// 按区间清除时只部分重叠或时间无法解析的条目保留。
func (s *Store) ClearUnrecoverable(ctx context.Context, spaceID, datasetID string, start, end time.Time) ([]Unrecoverable, error) {
	var removed []Unrecoverable
	clearAll := start.IsZero() && end.IsZero()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var current string
		err := tx.QueryRowContext(ctx, `SELECT c_unrecoverable_json FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var items, kept []Unrecoverable
		if current != "" && current != "[]" {
			if err := json.Unmarshal([]byte(current), &items); err != nil {
				return fmt.Errorf("解析任务的无法补齐区间: %w", err)
			}
		}
		if clearAll {
			removed = items
		} else {
			for _, item := range items {
				from, ferr := time.Parse(time.RFC3339, item.StartTime)
				until, uerr := time.Parse(time.RFC3339, item.EndTime)
				if ferr == nil && uerr == nil && !from.Before(start) && !until.After(end) {
					removed = append(removed, item)
					continue
				}
				kept = append(kept, item)
			}
		}
		if len(removed) == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_unrecoverable_json = ? WHERE c_space_id = ? AND c_dataset_id = ?`, mergeUnrecoverable(kept), spaceID, datasetID)
		return err
	})
	return removed, err
}

// 以下查询语句整条写成常量，避免在调用处拼接 SQL。
const (
	queryTasks1 = `SELECT ` + taskColumns + ` FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`
	queryTasks2 = `SELECT ` + taskColumns + ` FROM t_archive_tasks WHERE (? = '' OR c_space_id = ?)
ORDER BY c_space_id, c_dataset_id LIMIT ? OFFSET ?`
	queryTasksFollowable = `SELECT ` + taskColumns + ` FROM t_archive_tasks t
WHERE t.c_status = 'enabled' AND t.c_source_missing = 0
  AND NOT EXISTS (
    SELECT 1 FROM t_archive_runs r
    WHERE r.c_space_id = t.c_space_id AND r.c_dataset_id = t.c_dataset_id AND r.c_status IN ('queued', 'running')
  )
ORDER BY t.c_space_id, t.c_dataset_id`
	queryTaskByDir = `SELECT c_dataset_id FROM t_archive_tasks WHERE c_space_id = ? AND c_purpose = ? AND c_dir_name = ?`
)
