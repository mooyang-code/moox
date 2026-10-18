package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Task 是一个归档任务。
type Task struct {
	// ID 是自增主键，删除后不复用，用作任务实例标识。
	ID            int64
	SpaceID       string
	DatasetID     string
	Freq          string
	PartitionUnit string
	DataNodeID    string
	Retention     string
	// FilePrefix 是导出文件名前缀（数据集名去空格）；创建时确定，之后数据集改名也不变，已导出的文件不会改名。
	FilePrefix string
	// MetaJSON 是数据集元数据（名称、简介等）的 JSON，创建与每小时维护时从 Storage 刷新。
	MetaJSON         string
	ScheduleCron     string
	ScheduleTZ       string
	IntervalSeconds  int
	StartAt          time.Time
	BackfillFrom     time.Time
	Status           string
	NextRunAt        time.Time
	ConsecutiveSkips int
	LastSuccessAt    time.Time
	FinalUntil       time.Time
	Creator          string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const taskColumns = `c_id, c_space_id, c_dataset_id, c_freq, c_partition_unit, c_data_node_id, c_retention, c_file_prefix, c_meta_json, c_schedule_cron, c_schedule_tz,
c_interval_seconds, c_start_at, c_backfill_from, c_status, c_next_run_at, c_consecutive_skips, c_last_success_at, c_final_until,
c_creator, c_ctime, c_mtime`

type rowScanner interface{ Scan(dest ...any) error }

func scanTask(row rowScanner) (Task, error) {
	var t Task
	var start, backfill, next, lastSuccess, finalUntil, created, updated timeValue
	err := row.Scan(&t.ID, &t.SpaceID, &t.DatasetID, &t.Freq, &t.PartitionUnit, &t.DataNodeID, &t.Retention, &t.FilePrefix, &t.MetaJSON, &t.ScheduleCron, &t.ScheduleTZ,
		&t.IntervalSeconds, &start, &backfill, &t.Status, &next, &t.ConsecutiveSkips, &lastSuccess, &finalUntil,
		&t.Creator, &created, &updated)
	if err != nil {
		return Task{}, err
	}
	t.StartAt, t.BackfillFrom, t.NextRunAt = start.Time, backfill.Time, next.Time
	t.LastSuccessAt, t.FinalUntil, t.CreatedAt, t.UpdatedAt = lastSuccess.Time, finalUntil.Time, created.Time, updated.Time
	return t, nil
}

// CreateTask 创建任务；immediate 为真时在同一事务里生成一条 manual 运行。数据集已有任务返回 ErrConflict。
func (s *Store) CreateTask(ctx context.Context, task Task, immediate bool, now time.Time) (Task, *Run, error) {
	var run *Run
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO t_archive_tasks (c_space_id, c_dataset_id, c_freq, c_partition_unit, c_data_node_id, c_retention, c_file_prefix, c_meta_json,
c_schedule_cron, c_schedule_tz, c_interval_seconds, c_start_at, c_backfill_from, c_status, c_next_run_at, c_consecutive_skips, c_creator)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			task.SpaceID, task.DatasetID, task.Freq, task.PartitionUnit, task.DataNodeID, task.Retention, task.FilePrefix, task.MetaJSON,
			task.ScheduleCron, task.ScheduleTZ, task.IntervalSeconds, fmtTime(task.StartAt), nullTime(task.BackfillFrom),
			task.Status, nullTime(task.NextRunAt), task.Creator)
		if err != nil {
			if isUnique(err) {
				return ErrConflict
			}
			return err
		}
		if immediate {
			created, err := insertRun(ctx, tx, task.SpaceID, task.DatasetID, KindManual, time.Time{}, time.Time{}, now)
			if err != nil {
				return err
			}
			run = &created
		}
		return nil
	})
	if err != nil {
		return Task{}, nil, err
	}
	created, err := s.GetTask(ctx, task.SpaceID, task.DatasetID)
	return created, run, err
}

// GetTask 读取一个任务。
func (s *Store) GetTask(ctx context.Context, spaceID, datasetID string) (Task, error) {
	task, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID))
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskColumns+` FROM t_archive_tasks WHERE (? = '' OR c_space_id = ?)
ORDER BY c_space_id, c_dataset_id LIMIT ? OFFSET ?`, spaceID, spaceID, limit, offset)
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

// TaskUpdate 是 UpdateTask 的修改内容；Status 为空表示不改状态。
type TaskUpdate struct {
	ScheduleCron    string
	ScheduleTZ      string
	IntervalSeconds int
	StartAt         time.Time
	BackfillFrom    time.Time
	Status          string
	NextRunAt       time.Time
	// RunNow 为真时在同一事务里生成一条 manual 运行（立即开始，或停用后重新启用时立即运行一次）。
	RunNow bool
}

// UpdateTask 修改任务；停用时同时取消排队中的运行。
func (s *Store) UpdateTask(ctx context.Context, spaceID, datasetID string, update TaskUpdate, now time.Time) (Task, *Run, error) {
	var run *Run
	err := s.tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_schedule_cron = ?, c_schedule_tz = ?, c_interval_seconds = ?, c_start_at = ?,
c_backfill_from = ?, c_status = ?, c_next_run_at = ?, c_consecutive_skips = 0 WHERE c_space_id = ? AND c_dataset_id = ?`,
			update.ScheduleCron, update.ScheduleTZ, update.IntervalSeconds, fmtTime(update.StartAt), nullTime(update.BackfillFrom),
			update.Status, nullTime(update.NextRunAt), spaceID, datasetID)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return ErrNotFound
		}
		if update.Status == StatusPaused {
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'canceled', c_finished_at = ?, c_message = '任务已停用'
WHERE c_space_id = ? AND c_dataset_id = ? AND c_status = 'queued'`, fmtTime(now), spaceID, datasetID); err != nil {
				return err
			}
		}
		if update.RunNow && update.Status == StatusEnabled {
			// 已有排队中或运行中的运行时不再生成：同一任务同时只能有一个运行，否则会并发改写同一个暂存区。
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_runs WHERE c_space_id = ? AND c_dataset_id = ? AND c_status IN ('queued', 'running')`,
				spaceID, datasetID).Scan(&active); err != nil {
				return err
			}
			if active == 0 {
				created, err := insertRun(ctx, tx, spaceID, datasetID, KindManual, time.Time{}, time.Time{}, now)
				if err != nil {
					return err
				}
				run = &created
			}
		}
		return nil
	})
	if err != nil {
		return Task{}, nil, err
	}
	task, err := s.GetTask(ctx, spaceID, datasetID)
	return task, run, err
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

// UpdateTaskDataset 刷新任务的 DataNode 与线上保留期（维护定时器每小时从 Storage 读取）。
func (s *Store) UpdateTaskDataset(ctx context.Context, spaceID, datasetID, dataNodeID, retention, metaJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE t_archive_tasks SET c_data_node_id = ?, c_retention = ?, c_meta_json = ? WHERE c_space_id = ? AND c_dataset_id = ?
AND (c_data_node_id != ? OR c_retention != ? OR c_meta_json != ?)`, dataNodeID, retention, metaJSON, spaceID, datasetID, dataNodeID, retention, metaJSON)
	return err
}

// TriggerResult 是一次全局触发的统计。
type TriggerResult struct {
	Created int
	Skipped int
}

// TriggerDue 处理所有到点的启用任务：已有排队中或运行中的运行时累加连续跳过次数，否则生成一条 scheduled 运行并清零；
// 然后把 c_next_run_at 推进到 now 之后的下一个计划时刻（错过的计划时刻不补）。
// next 对任务计算 now 之后的下一个计划时刻；返回错误时该任务的下一次推迟一小时，避免每秒重复失败。
func (s *Store) TriggerDue(ctx context.Context, now time.Time, next func(Task, time.Time) (time.Time, error)) (TriggerResult, []error, error) {
	var result TriggerResult
	var problems []error
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM t_archive_tasks WHERE c_status = 'enabled' AND c_next_run_at IS NOT NULL AND c_next_run_at <= ?
ORDER BY c_next_run_at`, fmtTime(now))
		if err != nil {
			return err
		}
		var due []Task
		for rows.Next() {
			task, err := scanTask(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			due = append(due, task)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, task := range due {
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_runs WHERE c_space_id = ? AND c_dataset_id = ? AND c_status IN ('queued', 'running')`,
				task.SpaceID, task.DatasetID).Scan(&active); err != nil {
				return err
			}
			if active > 0 {
				result.Skipped++
				if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_consecutive_skips = c_consecutive_skips + 1 WHERE c_space_id = ? AND c_dataset_id = ?`,
					task.SpaceID, task.DatasetID); err != nil {
					return err
				}
			} else {
				if _, err := insertRun(ctx, tx, task.SpaceID, task.DatasetID, KindScheduled, time.Time{}, time.Time{}, task.NextRunAt); err != nil {
					return err
				}
				result.Created++
				if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_consecutive_skips = 0 WHERE c_space_id = ? AND c_dataset_id = ?`, task.SpaceID, task.DatasetID); err != nil {
					return err
				}
			}
			following, err := next(task, now)
			if err == nil && following.IsZero() {
				err = errors.New("执行计划没有下一个计划时刻")
			}
			if err != nil {
				problems = append(problems, fmt.Errorf("task %s/%s: %w", task.SpaceID, task.DatasetID, err))
				following = now.Add(time.Hour)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_next_run_at = ? WHERE c_space_id = ? AND c_dataset_id = ?`,
				fmtTime(following), task.SpaceID, task.DatasetID); err != nil {
				return err
			}
		}
		return nil
	})
	return result, problems, err
}
