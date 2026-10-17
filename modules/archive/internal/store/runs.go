package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Unrecoverable 是超出线上保留期、无法补齐的区间。
type Unrecoverable struct {
	SubjectID string `json:"subject_id,omitempty"`
	SeriesTag string `json:"series_tag,omitempty"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

// maxUnrecoverable 是单次运行记录的无法补齐区间上限（按序列合并后）。
const maxUnrecoverable = 200

// RunStats 是运行的统计，进度与完成共用。
type RunStats struct {
	SubjectsTotal       uint64
	SubjectsDone        uint64
	RequestCount        uint64
	BytesRead           uint64
	RowsRead            uint64
	RowsAdded           uint64
	RowsChanged         uint64
	PartitionsExported  uint32
	PartitionsFinalized uint32
}

// Run 是一次归档运行。
type Run struct {
	RunID         string
	SpaceID       string
	DatasetID     string
	Kind          string
	RangeStart    time.Time
	RangeEnd      time.Time
	Status        string
	ScheduledAt   time.Time
	StartedAt     time.Time
	FinishedAt    time.Time
	WorkerID      string
	LeaseToken    string
	LeaseUntil    time.Time
	Stats         RunStats
	Unrecoverable []Unrecoverable
	Message       string
	CreatedAt     time.Time
}

const runColumns = `c_run_id, c_space_id, c_dataset_id, c_kind, c_range_start, c_range_end, c_status, c_scheduled_at, c_started_at, c_finished_at,
c_worker_id, c_lease_token, c_lease_until, c_subjects_total, c_subjects_done, c_request_count, c_bytes_read, c_rows_read, c_rows_added,
c_rows_changed, c_partitions_exported, c_partitions_finalized, c_unrecoverable_json, c_message, c_ctime`

func scanRun(row rowScanner) (Run, error) {
	var r Run
	var rangeStart, rangeEnd, scheduled, started, finished, leaseUntil, created timeValue
	var unrecoverable string
	err := row.Scan(&r.RunID, &r.SpaceID, &r.DatasetID, &r.Kind, &rangeStart, &rangeEnd, &r.Status, &scheduled, &started, &finished,
		&r.WorkerID, &r.LeaseToken, &leaseUntil, &r.Stats.SubjectsTotal, &r.Stats.SubjectsDone, &r.Stats.RequestCount, &r.Stats.BytesRead,
		&r.Stats.RowsRead, &r.Stats.RowsAdded, &r.Stats.RowsChanged, &r.Stats.PartitionsExported, &r.Stats.PartitionsFinalized,
		&unrecoverable, &r.Message, &created)
	if err != nil {
		return Run{}, err
	}
	r.RangeStart, r.RangeEnd, r.ScheduledAt, r.StartedAt, r.FinishedAt = rangeStart.Time, rangeEnd.Time, scheduled.Time, started.Time, finished.Time
	r.LeaseUntil, r.CreatedAt = leaseUntil.Time, created.Time
	if unrecoverable != "" && unrecoverable != "[]" {
		_ = json.Unmarshal([]byte(unrecoverable), &r.Unrecoverable)
	}
	return r, nil
}

func insertRun(ctx context.Context, tx *sql.Tx, spaceID, datasetID, kind string, rangeStart, rangeEnd, scheduledAt time.Time) (Run, error) {
	id := newID("run_")
	if _, err := tx.ExecContext(ctx, `INSERT INTO t_archive_runs (c_run_id, c_space_id, c_dataset_id, c_kind, c_range_start, c_range_end, c_status, c_scheduled_at)
VALUES (?, ?, ?, ?, ?, ?, 'queued', ?)`, id, spaceID, datasetID, kind, nullTime(rangeStart), nullTime(rangeEnd), fmtTime(scheduledAt)); err != nil {
		return Run{}, err
	}
	return scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_archive_runs WHERE c_run_id = ?`, id))
}

// CreateRun 为任务生成一条 manual 或 range 运行；已有排队中或运行中的运行时 manual 返回 ErrConflict。
func (s *Store) CreateRun(ctx context.Context, spaceID, datasetID, kind string, rangeStart, rangeEnd, now time.Time) (Run, error) {
	var run Run
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_runs WHERE c_space_id = ? AND c_dataset_id = ? AND c_status IN ('queued', 'running')`, spaceID, datasetID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrConflict
		}
		created, err := insertRun(ctx, tx, spaceID, datasetID, kind, rangeStart, rangeEnd, now)
		run = created
		return err
	})
	return run, err
}

// GetRun 读取一次运行。
func (s *Store) GetRun(ctx context.Context, runID string) (Run, error) {
	run, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_archive_runs WHERE c_run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return run, err
}

// ListRuns 按计划时刻倒序分页列出运行；spaceID、datasetID、status 为空表示不限。
func (s *Store) ListRuns(ctx context.Context, spaceID, datasetID, status string, limit, offset int) ([]Run, int, error) {
	where := `(? = '' OR c_space_id = ?) AND (? = '' OR c_dataset_id = ?) AND (? = '' OR c_status = ?)`
	args := []any{spaceID, spaceID, datasetID, datasetID, status, status}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_runs WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM t_archive_runs WHERE `+where+` ORDER BY c_scheduled_at DESC, c_id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, run)
	}
	return out, total, rows.Err()
}

// ListActiveRuns 返回排队中与运行中的运行。
func (s *Store) ListActiveRuns(ctx context.Context, spaceID string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM t_archive_runs WHERE c_status IN ('queued', 'running') AND (? = '' OR c_space_id = ?)
ORDER BY c_scheduled_at`, spaceID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// LastRuns 返回每个任务最近一次运行（按计划时刻），用于任务列表。
func (s *Store) LastRuns(ctx context.Context, spaceID string) (map[string]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM t_archive_runs r WHERE (? = '' OR c_space_id = ?)
AND c_id = (SELECT c_id FROM t_archive_runs x WHERE x.c_space_id = r.c_space_id AND x.c_dataset_id = r.c_dataset_id ORDER BY c_scheduled_at DESC, c_id DESC LIMIT 1)`, spaceID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out[run.SpaceID+"/"+run.DatasetID] = run
	}
	return out, rows.Err()
}

// CancelRun 取消运行：排队中的立即取消；运行中的标为 canceled，worker 在下一次心跳或进度上报时得知并停止。
// 已结束的运行返回 ErrConflict。spaceID 非空时要求运行属于该空间。
func (s *Store) CancelRun(ctx context.Context, runID, spaceID string, now time.Time) (Run, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		run, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_archive_runs WHERE c_run_id = ?`, runID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if spaceID != "" && run.SpaceID != spaceID {
			return ErrNotFound
		}
		if run.Status != RunQueued && run.Status != RunRunning {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'canceled', c_finished_at = ?, c_message = '已取消' WHERE c_run_id = ?`, fmtTime(now), runID)
		return err
	})
	if err != nil {
		return Run{}, err
	}
	return s.GetRun(ctx, runID)
}

// ClaimRequest 是 worker 领取运行的参数。
type ClaimRequest struct {
	WorkerID       string
	FreeSlots      int
	MaxPerDataNode int
	BusyByDataNode map[string]int
	Now            time.Time
	LeaseTTL       time.Duration
}

// Claimed 是被领取的运行和它所属的任务。
type Claimed struct {
	Run  Run
	Task Task
}

// ClaimRuns 在一个事务里按计划时刻从早到晚选出不超过空闲槽数和 DataNode 并发上限的排队运行，写入运行租约后返回。
// 已有分区登记的任务只派给分区登记的那个 worker_id。
func (s *Store) ClaimRuns(ctx context.Context, req ClaimRequest) ([]Claimed, error) {
	if req.FreeSlots <= 0 {
		return nil, nil
	}
	var claimed []Claimed
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+runColumns+` FROM t_archive_runs r WHERE c_status = 'queued'
AND NOT EXISTS (SELECT 1 FROM t_archive_partitions p WHERE p.c_space_id = r.c_space_id AND p.c_dataset_id = r.c_dataset_id AND p.c_worker_id != ?)
ORDER BY c_scheduled_at, c_id`, req.WorkerID)
		if err != nil {
			return err
		}
		var queued []Run
		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			queued = append(queued, run)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		busy := map[string]int{}
		for node, count := range req.BusyByDataNode {
			busy[node] = count
		}
		for _, run := range queued {
			if len(claimed) >= req.FreeSlots {
				break
			}
			task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`, run.SpaceID, run.DatasetID))
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			if req.MaxPerDataNode > 0 && busy[task.DataNodeID] >= req.MaxPerDataNode {
				continue
			}
			token := newToken()
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'running', c_started_at = ?, c_worker_id = ?, c_lease_token = ?, c_lease_until = ?
WHERE c_run_id = ? AND c_status = 'queued'`, fmtTime(req.Now), req.WorkerID, token, fmtTime(req.Now.Add(req.LeaseTTL)), run.RunID); err != nil {
				return err
			}
			run.Status, run.StartedAt, run.WorkerID, run.LeaseToken, run.LeaseUntil = RunRunning, req.Now, req.WorkerID, token, req.Now.Add(req.LeaseTTL)
			busy[task.DataNodeID]++
			claimed = append(claimed, Claimed{Run: run, Task: task})
		}
		return nil
	})
	return claimed, err
}

// Progress 续租并更新进度。运行已被取消、已结束或租约不属于调用方时返回 canceled=true。
func (s *Store) Progress(ctx context.Context, runID, workerID, leaseToken string, stats RunStats, now time.Time, ttl time.Duration) (canceled bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		run, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_archive_runs WHERE c_run_id = ?`, runID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if run.Status != RunRunning || run.LeaseToken != leaseToken || run.WorkerID != workerID {
			canceled = true
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_lease_until = ?, c_subjects_total = ?, c_subjects_done = ?, c_request_count = ?, c_bytes_read = ?, c_rows_read = ?
WHERE c_run_id = ?`, fmtTime(now.Add(ttl)), stats.SubjectsTotal, stats.SubjectsDone, stats.RequestCount, stats.BytesRead, stats.RowsRead, runID)
		return err
	})
	return canceled, err
}

// ExpireLeases 把租约已过期的运行标为失败（worker 失联），返回处理的条数。
func (s *Store) ExpireLeases(ctx context.Context, now time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'failed', c_finished_at = ?, c_message = 'worker 失联'
WHERE c_status = 'running' AND c_lease_until IS NOT NULL AND c_lease_until < ?`, fmtTime(now), fmtTime(now))
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// RunningRunIDsCanceled 返回给定运行 ID 中已被取消、已结束或不存在的（worker 应停止这些运行）。
func (s *Store) RunningRunIDsCanceled(ctx context.Context, workerID string, runIDs []string) ([]string, error) {
	var out []string
	for _, id := range runIDs {
		run, err := s.GetRun(ctx, id)
		if errors.Is(err, ErrNotFound) {
			out = append(out, id)
			continue
		}
		if err != nil {
			return nil, err
		}
		if run.Status != RunRunning || run.WorkerID != workerID {
			out = append(out, id)
		}
	}
	return out, nil
}

// CleanupRuns 按保留期清理运行记录：成功的定时运行用 scheduledSuccess，其余用 other。
func (s *Store) CleanupRuns(ctx context.Context, now time.Time, scheduledSuccess, other time.Duration) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM t_archive_runs WHERE c_status IN ('succeeded', 'failed', 'canceled') AND c_finished_at IS NOT NULL AND
((c_kind = 'scheduled' AND c_status = 'succeeded' AND c_finished_at < ?) OR (NOT (c_kind = 'scheduled' AND c_status = 'succeeded') AND c_finished_at < ?))`,
		fmtTime(now.Add(-scheduledSuccess)), fmtTime(now.Add(-other)))
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// CountQueued 返回排队中的运行数。
func (s *Store) CountQueued(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_runs WHERE c_status = 'queued'`).Scan(&n)
	return n, err
}

func mergeUnrecoverable(items []Unrecoverable) string {
	if len(items) > maxUnrecoverable {
		items = items[:maxUnrecoverable]
	}
	raw, err := json.Marshal(items)
	if err != nil || strings.TrimSpace(string(raw)) == "null" {
		return "[]"
	}
	return string(raw)
}
