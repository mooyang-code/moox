package staging

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ChangeCursor 返回任务读 Storage 变更日志的位点（已读完并写进暂存区的日志之后）；没有时 ok 为假。
func (s *Store) ChangeCursor(ctx context.Context, spaceID, datasetID string) (storeID string, sequence uint64, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT change_store_id, change_sequence FROM task_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID).Scan(&storeID, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	return storeID, sequence, err == nil && storeID != "", err
}

// SetChangeCursor 记下变更日志的位点。
func (s *Store) SetChangeCursor(ctx context.Context, spaceID, datasetID, storeID string, sequence uint64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, change_store_id, change_sequence) VALUES (?, ?, ?, ?)
ON CONFLICT (space_id, dataset_id) DO UPDATE SET change_store_id = excluded.change_store_id, change_sequence = excluded.change_sequence`,
		spaceID, datasetID, storeID, sequence)
	return err
}

// Rescan 返回整段重读的进度 [from, until)：还没读完的部分；不在重读时 ok 为假。
func (s *Store) Rescan(ctx context.Context, spaceID, datasetID string) (from, until time.Time, ok bool, err error) {
	var f, u sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT rescan_from, rescan_until FROM task_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID).Scan(&f, &u)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, time.Time{}, false, nil
	}
	if err != nil || !f.Valid || !u.Valid {
		return time.Time{}, time.Time{}, false, err
	}
	return fromNanos(f.Int64), fromNanos(u.Int64), true, nil
}

// SetRebase 在一个事务里重建基线：位点取日志头部，整段重读进度设为 [from, until)。先落位点再读数据：
// 位点之后的写入由之后的日志读取覆盖，之前的由整段重读覆盖。
func (s *Store) SetRebase(ctx context.Context, spaceID, datasetID, storeID string, sequence uint64, from, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, change_store_id, change_sequence, rescan_from, rescan_until) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (space_id, dataset_id) DO UPDATE SET change_store_id = excluded.change_store_id, change_sequence = excluded.change_sequence,
rescan_from = excluded.rescan_from, rescan_until = excluded.rescan_until`,
		spaceID, datasetID, storeID, sequence, nanos(from), nanos(until))
	return err
}

// AdvanceRescan 把整段重读进度推进到 to；done 为真时（最后一段已读完）两列清成 NULL。
func (s *Store) AdvanceRescan(ctx context.Context, spaceID, datasetID string, to time.Time, done bool) error {
	if done {
		_, err := s.db.ExecContext(ctx, `UPDATE task_state SET rescan_from = NULL, rescan_until = NULL WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE task_state SET rescan_from = ? WHERE space_id = ? AND dataset_id = ? AND rescan_from IS NOT NULL`,
		nanos(to), spaceID, datasetID)
	return err
}

// BindTask 把数据集的暂存状态绑定到任务 ID，并记下任务的用途与目录名。本地已绑定的 ID 与 taskID 不同，说明任务被删除后重建过：
// 旧任务遗留的位点、重读进度、暂存数据和分区状态一律丢弃，新任务从自己的设置开始。目录名建好后不改，同一任务 ID 不会变。
func (s *Store) BindTask(ctx context.Context, task Task, taskID string) error {
	if taskID == "" {
		return errors.New("运行缺少 task_id")
	}
	if task.Purpose == "" || task.DirName == "" {
		return errors.New("运行缺少用途或目录名")
	}
	var stored string
	err := s.db.QueryRowContext(ctx, `SELECT task_id FROM task_state WHERE space_id = ? AND dataset_id = ?`, task.SpaceID, task.DatasetID).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if stored == taskID {
		return nil
	}
	// 没有 task_state 记录（或未绑定）时也可能留有其他状态，一并清掉再绑定。
	if err := s.DropTask(ctx, task.SpaceID, task.DatasetID); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, task_id, purpose, dir_name) VALUES (?, ?, ?, ?, ?)`,
		task.SpaceID, task.DatasetID, taskID, task.Purpose, task.DirName)
	return err
}

// TaskDir 返回绑定任务时记下的用途与目录名；数据集没有绑定过任务时 ok 为假。
func (s *Store) TaskDir(ctx context.Context, spaceID, datasetID string) (purpose, dirName string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT purpose, dir_name FROM task_state WHERE space_id = ? AND dataset_id = ? AND task_id != ''`,
		spaceID, datasetID).Scan(&purpose, &dirName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return purpose, dirName, err == nil, err
}

// SyncedTask 表示暂存库已完成分区登记同步的任务（新建或丢失暂存库后为空，任务重建会换 task_id 并重置）。
type SyncedTask struct {
	SpaceID   string
	DatasetID string
	TaskID    string
}

// SyncedTasks 返回本地已完成已登记分区同步的任务列表，用于 PullArchiveRun 按需下发。
func (s *Store) SyncedTasks(ctx context.Context) ([]SyncedTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT space_id, dataset_id, task_id FROM task_state WHERE task_id != '' AND partitions_synced = 1 ORDER BY space_id, dataset_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncedTask
	for rows.Next() {
		var t SyncedTask
		if err := rows.Scan(&t.SpaceID, &t.DatasetID, &t.TaskID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Reconcile 按 mgr 下发的当前全部任务对账：keep 的键是 "space/dataset"，值是任务 ID。
// 本地有状态、但不在 keep 里（任务已删除）或任务 ID 不同（任务已重建）的数据集，删掉其暂存表与全部状态。返回删掉的数据集。
func (s *Store) Reconcile(ctx context.Context, keep map[string]string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT space_id, dataset_id, task_id FROM task_state
UNION SELECT DISTINCT space_id, dataset_id, '' FROM partition_state WHERE NOT EXISTS (
    SELECT 1 FROM task_state t WHERE t.space_id = partition_state.space_id AND t.dataset_id = partition_state.dataset_id)`)
	if err != nil {
		return nil, err
	}
	type ref struct{ space, dataset string }
	var drop []ref
	for rows.Next() {
		var space, dataset, taskID string
		if err := rows.Scan(&space, &dataset, &taskID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if want, ok := keep[space+"/"+dataset]; !ok || want != taskID {
			drop = append(drop, ref{space, dataset})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var dropped []string
	for _, r := range drop {
		if err := s.DropTask(ctx, r.space, r.dataset); err != nil {
			return dropped, fmt.Errorf("清理数据集 %s/%s 的暂存状态: %w", r.space, r.dataset, err)
		}
		dropped = append(dropped, r.space+"/"+r.dataset)
	}
	return dropped, nil
}
