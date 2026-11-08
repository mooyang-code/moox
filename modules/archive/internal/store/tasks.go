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
	Purpose       string
	DirName       string
	Freq          string
	PartitionUnit string
	DataNodeID    string
	Retention     string
	// MetaJSON 是数据集元数据（名称、简介等）的 JSON，创建与每小时维护时从 Storage 刷新。
	MetaJSON string
	// PullEveryBars 是 N：每收盘 N 根拉取一次（1d 及以上忽略）。
	PullEveryBars int
	// IntervalSeconds 是复核间隔：低于 1d 为 3600，1d 及以上为 86400。
	IntervalSeconds int
	BackfillFrom    time.Time
	Status          string
	// LastBoundary、LastReview 是最近一次已生成运行的拉取边界与复核边界；为空时下一次检查立即复核。
	LastBoundary     time.Time
	LastReview       time.Time
	ConsecutiveSkips int
	LastSuccessAt    time.Time
	FinalUntil       time.Time
	Creator          string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	// Unrecoverable 是按任务累积的无法补齐区间（超出线上保留期）。
	Unrecoverable []Unrecoverable
	// BlockedReason 是分区登记被拒的原因（行数或标的数倒退）；下一次被接受的完成上报清空。
	BlockedReason string
}

const taskColumns = `c_id, c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq, c_partition_unit, c_data_node_id, c_retention, c_meta_json, c_pull_every_bars,
c_interval_seconds, c_backfill_from, c_status, c_last_boundary, c_last_review, c_consecutive_skips, c_last_success_at, c_final_until,
c_creator, c_ctime, c_mtime, c_unrecoverable_json, c_blocked_reason`

type rowScanner interface{ Scan(dest ...any) error }

func scanTask(row rowScanner) (Task, error) {
	var t Task
	var backfill, lastBoundary, lastReview, lastSuccess, finalUntil, created, updated timeValue
	var unrecoverable string
	err := row.Scan(&t.ID, &t.SpaceID, &t.DatasetID, &t.Purpose, &t.DirName, &t.Freq, &t.PartitionUnit, &t.DataNodeID, &t.Retention, &t.MetaJSON, &t.PullEveryBars,
		&t.IntervalSeconds, &backfill, &t.Status, &lastBoundary, &lastReview, &t.ConsecutiveSkips, &lastSuccess, &finalUntil,
		&t.Creator, &created, &updated, &unrecoverable, &t.BlockedReason)
	if err != nil {
		return Task{}, err
	}
	if unrecoverable != "" && unrecoverable != "[]" {
		_ = json.Unmarshal([]byte(unrecoverable), &t.Unrecoverable)
	}
	t.BackfillFrom, t.LastBoundary, t.LastReview = backfill.Time, lastBoundary.Time, lastReview.Time
	t.LastSuccessAt, t.FinalUntil, t.CreatedAt, t.UpdatedAt = lastSuccess.Time, finalUntil.Time, created.Time, updated.Time
	return t, nil
}

// CreateTask 创建任务；两个边界为空，启用的任务在下一次检查时立即复核。
// 数据集已有任务返回 ErrConflict；目录名已被同空间同用途的其他任务使用返回 ErrDirNameTaken（检查与插入在同一事务里）。
func (s *Store) CreateTask(ctx context.Context, task Task) (Task, error) {
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
		_, err = tx.ExecContext(ctx, `INSERT INTO t_archive_tasks (c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq, c_partition_unit, c_data_node_id,
c_retention, c_meta_json, c_pull_every_bars, c_interval_seconds, c_backfill_from, c_status, c_consecutive_skips, c_creator)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			task.SpaceID, task.DatasetID, task.Purpose, task.DirName, task.Freq, task.PartitionUnit, task.DataNodeID,
			task.Retention, task.MetaJSON, task.PullEveryBars, task.IntervalSeconds, nullTime(task.BackfillFrom), task.Status, task.Creator)
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

// TaskUpdate 是 UpdateTask 的修改内容（完整的新值）。
type TaskUpdate struct {
	PullEveryBars int
	BackfillFrom  time.Time
	Status        string
	// ClearUnrecoverable 为真时清空累积的无法补齐区间。
	ClearUnrecoverable bool
}

// UpdateTask 修改任务；停用时同时取消排队中的运行；从停用改为启用时清空两个边界，下一次检查立即复核。
func (s *Store) UpdateTask(ctx context.Context, spaceID, datasetID string, update TaskUpdate, now time.Time) (Task, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// SET 右侧的 c_status 是修改前的值。
		result, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_pull_every_bars = ?, c_backfill_from = ?, c_status = ?, c_consecutive_skips = 0,
c_last_boundary = CASE WHEN c_status = 'paused' AND ? = 'enabled' THEN NULL ELSE c_last_boundary END,
c_last_review = CASE WHEN c_status = 'paused' AND ? = 'enabled' THEN NULL ELSE c_last_review END,
c_unrecoverable_json = CASE WHEN ? THEN '[]' ELSE c_unrecoverable_json END WHERE c_space_id = ? AND c_dataset_id = ?`,
			update.PullEveryBars, nullTime(update.BackfillFrom), update.Status, update.Status, update.Status, update.ClearUnrecoverable, spaceID, datasetID)
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

// UpdateTaskDataset 刷新任务的 DataNode 与线上保留期（维护定时器每小时从 Storage 读取）。
func (s *Store) UpdateTaskDataset(ctx context.Context, spaceID, datasetID, dataNodeID, retention, metaJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE t_archive_tasks SET c_data_node_id = ?, c_retention = ?, c_meta_json = ? WHERE c_space_id = ? AND c_dataset_id = ?
AND (c_data_node_id != ? OR c_retention != ? OR c_meta_json != ?)`, dataNodeID, retention, metaJSON, spaceID, datasetID, dataNodeID, retention, metaJSON)
	return err
}

// TriggerResult 是一次检查的统计。
type TriggerResult struct {
	Reviews int
	Ticks   int
	Skipped int
}

// TriggerPlan 是一次检查对一个任务的判定：Kind 为空表示不到点；tick 带读取区间；两个边界是生成运行后要记下的值。
type TriggerPlan struct {
	Kind         string
	RangeStart   time.Time
	RangeEnd     time.Time
	LastBoundary time.Time
	LastReview   time.Time
}

// TriggerDue 在一个事务里处理全部启用的任务：plan 判定每个任务是否到点；不到点什么都不写；
// 到点但已有排队中或运行中的运行时累加连续跳过次数、边界不前进；否则生成运行、记下两个边界并清零跳过次数。
// plan 对某个任务返回错误时只跳过该任务并记一条问题。
func (s *Store) TriggerDue(ctx context.Context, now time.Time, plan func(Task) (TriggerPlan, error)) (TriggerResult, []error, error) {
	var result TriggerResult
	var problems []error
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// 先读完全部任务再写：库只有一个连接，读游标未关闭时不能执行写入。
		rows, err := tx.QueryContext(ctx, queryTasks3)
		if err != nil {
			return err
		}
		var enabled []Task
		for rows.Next() {
			task, err := scanTask(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			enabled = append(enabled, task)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		// 一次查出有活动运行的任务（走部分索引 idx_t_archive_runs_active），不要每个任务各扫一遍自己的运行记录。
		busy := map[string]bool{}
		activeRows, err := tx.QueryContext(ctx, `SELECT c_space_id, c_dataset_id FROM t_archive_runs WHERE c_status IN ('queued', 'running')`)
		if err != nil {
			return err
		}
		for activeRows.Next() {
			var space, dataset string
			if err := activeRows.Scan(&space, &dataset); err != nil {
				_ = activeRows.Close()
				return err
			}
			busy[space+"/"+dataset] = true
		}
		if err := activeRows.Close(); err != nil {
			return err
		}
		for _, task := range enabled {
			p, err := plan(task)
			if err != nil {
				problems = append(problems, fmt.Errorf("task %s/%s: %w", task.SpaceID, task.DatasetID, err))
				continue
			}
			if p.Kind == "" {
				continue
			}
			if p.Kind != KindScheduled && p.Kind != KindTick {
				problems = append(problems, fmt.Errorf("task %s/%s: 触发器不能生成 %q 运行", task.SpaceID, task.DatasetID, p.Kind))
				continue
			}
			if busy[task.SpaceID+"/"+task.DatasetID] {
				result.Skipped++
				if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_consecutive_skips = c_consecutive_skips + 1 WHERE c_id = ?`, task.ID); err != nil {
					return err
				}
				continue
			}
			if _, err := insertRun(ctx, tx, task.SpaceID, task.DatasetID, p.Kind, p.RangeStart, p.RangeEnd, now); err != nil {
				return err
			}
			if p.Kind == KindTick {
				result.Ticks++
			} else {
				result.Reviews++
			}
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_last_boundary = ?, c_last_review = ?, c_consecutive_skips = 0 WHERE c_id = ?`,
				nullTime(p.LastBoundary), nullTime(p.LastReview), task.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return result, problems, err
}

// 以下查询语句整条写成常量，避免在调用处拼接 SQL。
const (
	queryTasks1 = `SELECT ` + taskColumns + ` FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`
	queryTasks2 = `SELECT ` + taskColumns + ` FROM t_archive_tasks WHERE (? = '' OR c_space_id = ?)
ORDER BY c_space_id, c_dataset_id LIMIT ? OFFSET ?`
	queryTasks3    = `SELECT ` + taskColumns + ` FROM t_archive_tasks WHERE c_status = 'enabled' ORDER BY c_space_id, c_dataset_id`
	queryTaskByDir = `SELECT c_dataset_id FROM t_archive_tasks WHERE c_space_id = ? AND c_purpose = ? AND c_dir_name = ?`
)
