package staging

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// taskTime 读取 task_state 的一个时间列；没有记录或为 NULL 时 ok 为假。
func (s *Store) taskTime(ctx context.Context, column, spaceID, datasetID string) (time.Time, bool, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT `+column+` FROM task_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if !v.Valid {
		return time.Time{}, false, nil
	}
	return fromNanos(v.Int64), true, nil
}

// Watermark 返回任务的读取水位 W（上一次完整成功的定时或手动运行的开始时间）；没有时 ok 为假。
func (s *Store) Watermark(ctx context.Context, spaceID, datasetID string) (time.Time, bool, error) {
	return s.taskTime(ctx, "scan_watermark", spaceID, datasetID)
}

// FirstStart 返回首次运行（还没有水位）时记下的读取起点。
func (s *Store) FirstStart(ctx context.Context, spaceID, datasetID string) (time.Time, bool, error) {
	return s.taskTime(ctx, "first_start", spaceID, datasetID)
}

// ResumeFrom 返回运行中途记下的续读起点：之前的分区块已经导出、能定稿的已定稿，失败重跑从这里读起。
func (s *Store) ResumeFrom(ctx context.Context, spaceID, datasetID string) (time.Time, bool, error) {
	return s.taskTime(ctx, "resume_from", spaceID, datasetID)
}

// SetFirstStart 记下首次运行的读取起点；已有更早的起点时保留更早的。首次运行失败后重试必须从同一个起点读起，
// 否则会跳过两次起点之间尚未读到的数据。
func (s *Store) SetFirstStart(ctx context.Context, spaceID, datasetID string, start time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, first_start) VALUES (?, ?, ?)
ON CONFLICT (space_id, dataset_id) DO UPDATE SET first_start = CASE WHEN task_state.first_start IS NULL OR excluded.first_start < task_state.first_start THEN excluded.first_start ELSE task_state.first_start END`,
		spaceID, datasetID, nanos(start))
	return err
}

// SetResumeFrom 把续读起点推进到 at；只前进，不后退。
func (s *Store) SetResumeFrom(ctx context.Context, spaceID, datasetID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, resume_from) VALUES (?, ?, ?)
ON CONFLICT (space_id, dataset_id) DO UPDATE SET resume_from = CASE WHEN task_state.resume_from IS NULL OR excluded.resume_from > task_state.resume_from THEN excluded.resume_from ELSE task_state.resume_from END`,
		spaceID, datasetID, nanos(at))
	return err
}

// AdvanceWatermark 把读取水位推进到 watermark，并清掉首次起点与续读起点（水位前进后它们都不再需要）。
func (s *Store) AdvanceWatermark(ctx context.Context, spaceID, datasetID string, watermark time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, scan_watermark) VALUES (?, ?, ?)
ON CONFLICT (space_id, dataset_id) DO UPDATE SET scan_watermark = excluded.scan_watermark, first_start = NULL, resume_from = NULL`,
		spaceID, datasetID, nanos(watermark))
	return err
}

// BindTask 把数据集的暂存状态绑定到任务 ID，并记下任务的用途与目录名。本地已绑定的 ID 与 taskID 不同，说明任务被删除后重建过：
// 旧任务遗留的水位、起点、暂存数据和分区状态一律丢弃，新任务从自己的设置开始。目录名建好后不改，同一任务 ID 不会变。
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
