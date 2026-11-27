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
	Retention     string
	// MetaJSON 是数据集元数据（名称、简介等）的 JSON，创建与每小时维护时从 Storage 刷新。
	MetaJSON string
	// BackfillFrom 是归档起点：创建时确定，之后不改；暂存区没有分区时整段重读从这里读起。
	BackfillFrom time.Time
	Status       string
	// LastSuccessAt 是最近一次成功的 follow 或 manual 的开始时间。
	LastSuccessAt time.Time
	FinalUntil    time.Time
	Creator       string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	// Unrecoverable 是按任务累积的无法补齐区间（超出线上保留期）。
	Unrecoverable []Unrecoverable
	// BlockedReason 是分区登记被拒的原因（行数或标的数倒退）；下一次被接受的完成上报清空。
	BlockedReason string
}

const taskColumns = `c_id, c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq, c_partition_unit, c_retention, c_meta_json,
c_backfill_from, c_status, c_last_success_at, c_final_until, c_creator, c_ctime, c_mtime, c_unrecoverable_json, c_blocked_reason`

type rowScanner interface{ Scan(dest ...any) error }

func scanTask(row rowScanner) (Task, error) {
	var t Task
	var backfill, lastSuccess, finalUntil, created, updated timeValue
	var unrecoverable string
	err := row.Scan(&t.ID, &t.SpaceID, &t.DatasetID, &t.Purpose, &t.DirName, &t.Freq, &t.PartitionUnit, &t.Retention, &t.MetaJSON,
		&backfill, &t.Status, &lastSuccess, &finalUntil, &t.Creator, &created, &updated, &unrecoverable, &t.BlockedReason)
	if err != nil {
		return Task{}, err
	}
	if unrecoverable != "" && unrecoverable != "[]" {
		_ = json.Unmarshal([]byte(unrecoverable), &t.Unrecoverable)
	}
	t.BackfillFrom = backfill.Time
	t.LastSuccessAt, t.FinalUntil, t.CreatedAt, t.UpdatedAt = lastSuccess.Time, finalUntil.Time, created.Time, updated.Time
	return t, nil
}

// CreateTask 创建任务；BackfillFrom 必须非零，启用的任务从下一分钟起由 follow 运行跟随。
// 数据集已有任务返回 ErrConflict；目录名已被同空间同用途的其他任务使用返回 ErrDirNameTaken（检查与插入在同一事务里）。
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
		_, err = tx.ExecContext(ctx, `INSERT INTO t_archive_tasks (c_space_id, c_dataset_id, c_purpose, c_dir_name, c_freq, c_partition_unit,
c_retention, c_meta_json, c_backfill_from, c_status, c_creator)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			task.SpaceID, task.DatasetID, task.Purpose, task.DirName, task.Freq, task.PartitionUnit,
			task.Retention, task.MetaJSON, millisOf(task.BackfillFrom), task.Status, task.Creator)
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
	Status string
	// ClearUnrecoverable 为真时清空累积的无法补齐区间。
	ClearUnrecoverable bool
}

// UpdateTask 修改任务状态；停用时同时取消排队中的运行，重新启用后从原位点继续跟随。
func (s *Store) UpdateTask(ctx context.Context, spaceID, datasetID string, update TaskUpdate, now time.Time) (Task, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_status = ?,
c_unrecoverable_json = CASE WHEN ? THEN '[]' ELSE c_unrecoverable_json END WHERE c_space_id = ? AND c_dataset_id = ?`,
			update.Status, update.ClearUnrecoverable, spaceID, datasetID)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return ErrNotFound
		}
		if update.Status == StatusPaused {
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'canceled', c_finished_at = ?, c_message = '任务已停用'
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

// UpdateTaskDataset 刷新任务的线上保留期与数据集元数据（维护定时器每小时从 Storage 读取）。
func (s *Store) UpdateTaskDataset(ctx context.Context, spaceID, datasetID, retention, metaJSON string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE t_archive_tasks SET c_retention = ?, c_meta_json = ? WHERE c_space_id = ? AND c_dataset_id = ?
AND (c_retention != ? OR c_meta_json != ?)`, retention, metaJSON, spaceID, datasetID, retention, metaJSON)
	return err
}

// CreateFollowRuns 在一个事务里给每个启用、且没有排队中或运行中运行的任务生成一条 follow 运行，返回生成的条数。
// 有活动运行（含排队中的导入）的任务这一分钟什么都不写。
func (s *Store) CreateFollowRuns(ctx context.Context, now time.Time) (int, error) {
	created := 0
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
			if busy[task.SpaceID+"/"+task.DatasetID] {
				continue
			}
			if _, err := insertRun(ctx, tx, task.SpaceID, task.DatasetID, RunSpec{Kind: KindFollow}, now); err != nil {
				return err
			}
			created++
		}
		return nil
	})
	return created, err
}

// 以下查询语句整条写成常量，避免在调用处拼接 SQL。
const (
	queryTasks1 = `SELECT ` + taskColumns + ` FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`
	queryTasks2 = `SELECT ` + taskColumns + ` FROM t_archive_tasks WHERE (? = '' OR c_space_id = ?)
ORDER BY c_space_id, c_dataset_id LIMIT ? OFFSET ?`
	queryTasks3    = `SELECT ` + taskColumns + ` FROM t_archive_tasks WHERE c_status = 'enabled' ORDER BY c_space_id, c_dataset_id`
	queryTaskByDir = `SELECT c_dataset_id FROM t_archive_tasks WHERE c_space_id = ? AND c_purpose = ? AND c_dir_name = ?`
)

// ClearUnrecoverable 从任务累积的无法补齐区间里删掉整段落在 [start, end) 里的（起点不早于 start、终点不晚于 end），返回删掉的条目。
// 只部分重叠的区间保留：区间不带标的，范围判断只能由人确认后按整段清。时间无法解析的条目保留。
func (s *Store) ClearUnrecoverable(ctx context.Context, spaceID, datasetID string, start, end time.Time) ([]Unrecoverable, error) {
	var removed []Unrecoverable
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
		for _, item := range items {
			from, ferr := time.Parse(time.RFC3339, item.StartTime)
			until, uerr := time.Parse(time.RFC3339, item.EndTime)
			if ferr == nil && uerr == nil && !from.Before(start) && !until.After(end) {
				removed = append(removed, item)
				continue
			}
			kept = append(kept, item)
		}
		if len(removed) == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_unrecoverable_json = ? WHERE c_space_id = ? AND c_dataset_id = ?`, mergeUnrecoverable(kept), spaceID, datasetID)
		return err
	})
	return removed, err
}
