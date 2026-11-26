package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	// PartitionsUploaded 是本次运行上传到对象存储并校验通过的分区数。
	PartitionsUploaded uint32
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
	LeaseToken    string
	LeaseUntil    time.Time
	Stats         RunStats
	Unrecoverable []Unrecoverable
	Message       string
	CreatedAt     time.Time
}

const runColumns = `c_run_id, c_space_id, c_dataset_id, c_kind, c_range_start, c_range_end, c_status, c_scheduled_at, c_started_at, c_finished_at,
c_lease_token, c_lease_until, c_subjects_total, c_subjects_done, c_request_count, c_bytes_read, c_rows_read, c_rows_added,
c_rows_changed, c_partitions_exported, c_partitions_finalized, c_unrecoverable_json, c_message, c_ctime, c_partitions_uploaded`

func scanRun(row rowScanner) (Run, error) {
	var r Run
	var rangeStart, rangeEnd, scheduled, started, finished, leaseUntil, created timeValue
	var unrecoverable string
	err := row.Scan(&r.RunID, &r.SpaceID, &r.DatasetID, &r.Kind, &rangeStart, &rangeEnd, &r.Status, &scheduled, &started, &finished,
		&r.LeaseToken, &leaseUntil, &r.Stats.SubjectsTotal, &r.Stats.SubjectsDone, &r.Stats.RequestCount, &r.Stats.BytesRead,
		&r.Stats.RowsRead, &r.Stats.RowsAdded, &r.Stats.RowsChanged, &r.Stats.PartitionsExported, &r.Stats.PartitionsFinalized,
		&unrecoverable, &r.Message, &created, &r.Stats.PartitionsUploaded)
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

// RunSpec 是新运行的类型与参数：range 带读取区间。
type RunSpec struct {
	Kind       string
	RangeStart time.Time
	RangeEnd   time.Time
}

func insertRun(ctx context.Context, tx *sql.Tx, spaceID, datasetID string, spec RunSpec, scheduledAt time.Time) (Run, error) {
	id := newID("run_")
	if _, err := tx.ExecContext(ctx, `INSERT INTO t_archive_runs (c_run_id, c_space_id, c_dataset_id, c_kind, c_range_start, c_range_end, c_status, c_scheduled_at)
VALUES (?, ?, ?, ?, ?, ?, 'queued', ?)`, id, spaceID, datasetID, spec.Kind, nullTime(spec.RangeStart), nullTime(spec.RangeEnd), fmtTime(scheduledAt)); err != nil {
		return Run{}, err
	}
	return scanRun(tx.QueryRowContext(ctx, queryRuns1, id))
}

// ConflictError 是 CreateRun 冲突时挡住新运行的那条运行（errors.Is(err, ErrConflict) 为真）。
type ConflictError struct{ Blocking Run }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("任务已有%s的 %s 运行 %s", runStatusText(e.Blocking.Status), e.Blocking.Kind, e.Blocking.RunID)
}

func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

func runStatusText(status string) string {
	if status == RunRunning {
		return "运行中"
	}
	return "排队中"
}

// CreateRun 为任务生成一条 manual、range 或 import 运行（follow 只由 CreateFollowRuns 生成）：
// 先取消本任务排队中的 follow；本任务有排队中或运行中的 manual、range、import 时返回 *ConflictError；
// 运行中的 follow 不挡，新运行排在它后面（ClaimRuns 保证同一任务同时只执行一条）。
func (s *Store) CreateRun(ctx context.Context, spaceID, datasetID string, spec RunSpec, now time.Time) (Run, error) {
	switch spec.Kind {
	case KindManual, KindRange, KindImport:
	default:
		return Run{}, fmt.Errorf("CreateRun 不能生成 %q 运行", spec.Kind)
	}
	var run Run
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
		// 排队中的 follow 让位：取消它，下一分钟的 follow 在新运行结束后再生成。
		if _, err := tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'canceled', c_finished_at = ?, c_message = '让位给整段重读、补拉区间或导入'
WHERE c_space_id = ? AND c_dataset_id = ? AND c_kind = 'follow' AND c_status = 'queued'`, fmtTime(now), spaceID, datasetID); err != nil {
			return err
		}
		blocking, err := scanRun(tx.QueryRowContext(ctx, queryRunsBlocking, spaceID, datasetID))
		switch {
		case err == nil:
			return &ConflictError{Blocking: blocking}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		created, err := insertRun(ctx, tx, spaceID, datasetID, spec, now)
		run = created
		return err
	})
	return run, err
}

// GetRun 读取一次运行。
func (s *Store) GetRun(ctx context.Context, runID string) (Run, error) {
	run, err := scanRun(s.db.QueryRowContext(ctx, queryRuns1, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return run, err
}

// ListRuns 按计划时刻倒序分页列出运行；spaceID、datasetID、status 为空表示不限；includeFollow 为假时不列成功的 follow。
func (s *Store) ListRuns(ctx context.Context, spaceID, datasetID, status string, includeFollow bool, limit, offset int) ([]Run, int, error) {
	args := []any{spaceID, spaceID, datasetID, datasetID, status, status, includeFollow}
	var total int
	if err := s.db.QueryRowContext(ctx, queryRunsCount, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, queryRunsList, append(args, limit, offset)...)
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
	rows, err := s.db.QueryContext(ctx, queryRuns3, spaceID, spaceID)
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
	return s.lastRuns(ctx, queryRuns4, spaceID)
}

// LastFollowSuccesses 返回每个任务最近一次成功的 follow 或 manual（按计划时刻），健康规则据它的消息判断整段重读是否进行中。
func (s *Store) LastFollowSuccesses(ctx context.Context, spaceID string) (map[string]Run, error) {
	return s.lastRuns(ctx, queryRunsFollowSuccess, spaceID)
}

func (s *Store) lastRuns(ctx context.Context, query, spaceID string) (map[string]Run, error) {
	rows, err := s.db.QueryContext(ctx, query, spaceID, spaceID)
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
		run, err := scanRun(tx.QueryRowContext(ctx, queryRuns1, runID))
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
	FreeSlots int
	// MaxStorageRuns 是读 Storage 的运行（导入除外）同时运行的上限，0 表示不限；BusyStorageRuns 是 worker 已在运行的这类运行数。
	MaxStorageRuns  int
	BusyStorageRuns int
	Now             time.Time
	LeaseTTL        time.Duration
}

// Claimed 是被领取的运行和它所属的任务。
type Claimed struct {
	Run  Run
	Task Task
}

// ClaimRuns 在一个事务里按计划时刻从早到晚选出不超过空闲槽数和读 Storage 并发上限的排队运行，写入运行租约后返回。
func (s *Store) ClaimRuns(ctx context.Context, req ClaimRequest) ([]Claimed, error) {
	if req.FreeSlots <= 0 {
		return nil, nil
	}
	var claimed []Claimed
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, queryRuns6)
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
		busy := req.BusyStorageRuns
		// 同一任务同时只执行一条运行：新运行可以排在运行中的 follow 后面，领取时跳过已有运行中运行的任务（含本次刚领到的）。
		runningTasks := map[string]bool{}
		taskRows, err := tx.QueryContext(ctx, `SELECT DISTINCT c_space_id, c_dataset_id FROM t_archive_runs WHERE c_status = 'running'`)
		if err != nil {
			return err
		}
		for taskRows.Next() {
			var space, dataset string
			if err := taskRows.Scan(&space, &dataset); err != nil {
				_ = taskRows.Close()
				return err
			}
			runningTasks[space+"/"+dataset] = true
		}
		if err := taskRows.Close(); err != nil {
			return err
		}
		for _, run := range queued {
			if len(claimed) >= req.FreeSlots {
				break
			}
			if runningTasks[run.SpaceID+"/"+run.DatasetID] {
				continue
			}
			task, err := scanTask(tx.QueryRowContext(ctx, queryRuns7, run.SpaceID, run.DatasetID))
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			// 导入的行由 moox-cli 经数据端口写来，不读 Storage 时序数据（只取一次列信息），不占读 Storage 的并发名额。
			readsStorage := run.Kind != KindImport
			if readsStorage && req.MaxStorageRuns > 0 && busy >= req.MaxStorageRuns {
				continue
			}
			token := newToken()
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'running', c_started_at = ?, c_lease_token = ?, c_lease_until = ?
WHERE c_run_id = ? AND c_status = 'queued'`, fmtTime(req.Now), token, fmtTime(req.Now.Add(req.LeaseTTL)), run.RunID); err != nil {
				return err
			}
			run.Status, run.StartedAt, run.LeaseToken, run.LeaseUntil = RunRunning, req.Now, token, req.Now.Add(req.LeaseTTL)
			if readsStorage {
				busy++
			}
			runningTasks[run.SpaceID+"/"+run.DatasetID] = true
			claimed = append(claimed, Claimed{Run: run, Task: task})
		}
		return nil
	})
	return claimed, err
}

// Progress 续租并更新进度。运行已被取消、已结束或租约令牌不符时返回 canceled=true。
func (s *Store) Progress(ctx context.Context, runID, leaseToken string, stats RunStats, now time.Time, ttl time.Duration) (canceled bool, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		run, err := scanRun(tx.QueryRowContext(ctx, queryRuns1, runID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if run.Status != RunRunning || run.LeaseToken != leaseToken {
			canceled = true
			return nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_lease_until = ?, c_subjects_total = ?, c_subjects_done = ?, c_request_count = ?, c_bytes_read = ?, c_rows_read = ?
WHERE c_run_id = ?`, fmtTime(now.Add(ttl)), stats.SubjectsTotal, stats.SubjectsDone, stats.RequestCount, stats.BytesRead, stats.RowsRead, runID)
		return err
	})
	return canceled, err
}

// 租约过期的失败原因。worker 还在心跳却没续租，多半是领取的响应没送到 worker（mgr 已记开始、worker 不知道这条运行），
// 或 worker 重启后丢了这条运行；心跳也停了才是失联。
const (
	LeaseExpiredWorkerOffline = "worker 失联：租约到期时 worker 已停止心跳"
	LeaseExpiredWorkerOnline  = "租约到期时 worker 仍在心跳，但没有续租这条运行：领取的响应可能没送到 worker（网络超时），或 worker 重启后丢了这条运行"
)

// ExpireLeases 把租约已过期的运行标为失败，返回处理的条数；workerOnline 是 worker 此刻是否在线，决定失败原因。
func (s *Store) ExpireLeases(ctx context.Context, now time.Time, workerOnline bool) (int, error) {
	message := LeaseExpiredWorkerOffline
	if workerOnline {
		message = LeaseExpiredWorkerOnline
	}
	result, err := s.db.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'failed', c_finished_at = ?, c_message = ?
WHERE c_status = 'running' AND c_lease_until IS NOT NULL AND c_lease_until < ?`, fmtTime(now), message, fmtTime(now))
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// RunningRunIDsCanceled 返回给定运行 ID 中已被取消、已结束或不存在的（worker 应停止这些运行）。
func (s *Store) RunningRunIDsCanceled(ctx context.Context, runIDs []string) ([]string, error) {
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
		if run.Status != RunRunning {
			out = append(out, id)
		}
	}
	return out, nil
}

// CleanupRuns 按保留期清理运行记录：成功的 follow 用 followSuccess，失败与取消的 follow 用 followOtherRetention，其余用 other。
func (s *Store) CleanupRuns(ctx context.Context, now time.Time, followSuccess, other time.Duration) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM t_archive_runs WHERE c_status IN ('succeeded', 'failed', 'canceled') AND c_finished_at IS NOT NULL AND
((c_kind = 'follow' AND c_status = 'succeeded' AND c_finished_at < ?)
OR (c_kind = 'follow' AND c_status != 'succeeded' AND c_finished_at < ?)
OR (c_kind != 'follow' AND c_finished_at < ?))`,
		fmtTime(now.Add(-followSuccess)), fmtTime(now.Add(-followOtherRetention)), fmtTime(now.Add(-other)))
	if err != nil {
		return 0, err
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// followOtherRetention 是失败、取消的 follow 的保留期：每分钟可能一条，按其余运行的 90 天保留会堆积十几万条，拖慢每分钟的检查。
const followOtherRetention = 7 * 24 * time.Hour

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

// 以下查询语句整条写成常量，避免在调用处拼接 SQL。
const (
	queryRuns1 = `SELECT ` + runColumns + ` FROM t_archive_runs WHERE c_run_id = ?`
	queryRuns3 = `SELECT ` + runColumns + ` FROM t_archive_runs WHERE c_status IN ('queued', 'running') AND (? = '' OR c_space_id = ?)
ORDER BY c_scheduled_at`
	// 最近一次运行：从任务表出发（每个任务一次索引查找），不随运行记录（每分钟一条 follow）变多而变慢。
	queryRuns4 = `SELECT ` + runColumns + ` FROM t_archive_runs WHERE c_id IN (SELECT (SELECT x.c_id FROM t_archive_runs x
WHERE x.c_space_id = t.c_space_id AND x.c_dataset_id = t.c_dataset_id ORDER BY x.c_scheduled_at DESC, x.c_id DESC LIMIT 1)
FROM t_archive_tasks t WHERE (? = '' OR t.c_space_id = ?))`
	queryRunsFollowSuccess = `SELECT ` + runColumns + ` FROM t_archive_runs WHERE c_id IN (SELECT (SELECT x.c_id FROM t_archive_runs x
WHERE x.c_space_id = t.c_space_id AND x.c_dataset_id = t.c_dataset_id AND x.c_kind IN ('follow', 'manual') AND x.c_status = 'succeeded'
ORDER BY x.c_scheduled_at DESC, x.c_id DESC LIMIT 1)
FROM t_archive_tasks t WHERE (? = '' OR t.c_space_id = ?))`
	// 挡住新的 manual、range、import 的运行（排队中或运行中、不是 follow 的）。
	queryRunsBlocking = `SELECT ` + runColumns + ` FROM t_archive_runs WHERE c_space_id = ? AND c_dataset_id = ?
AND c_status IN ('queued', 'running') AND c_kind != 'follow' ORDER BY c_scheduled_at LIMIT 1`
	queryRuns6 = `SELECT ` + runColumns + ` FROM t_archive_runs WHERE c_status = 'queued' ORDER BY c_scheduled_at, c_id`
	queryRuns7 = `SELECT ` + taskColumns + ` FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`
)

// 运行列表的筛选条件固定，spaceID、datasetID、status 为空表示不限。
const (
	runsFilter = `(? = '' OR c_space_id = ?) AND (? = '' OR c_dataset_id = ?) AND (? = '' OR c_status = ?)
AND (? OR c_kind != 'follow' OR c_status != 'succeeded')`
	queryRunsCount = `SELECT COUNT(*) FROM t_archive_runs WHERE ` + runsFilter
	queryRunsList  = `SELECT ` + runColumns + ` FROM t_archive_runs WHERE ` + runsFilter + ` ORDER BY c_scheduled_at DESC, c_id DESC LIMIT ? OFFSET ?`
)
