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

// Partition 是一个任务的一个分区登记。
type Partition struct {
	SpaceID       string
	DatasetID     string
	PartitionKey  string
	RangeStart    time.Time
	RangeEnd      time.Time
	Status        string
	RowCount      uint64
	SubjectCount  uint64
	MinDataTime   time.Time
	MaxDataTime   time.Time
	FilePath      string
	FileBytes     uint64
	FileSHA256    string
	Columns       []string
	Preview       string
	ExportCount   uint32
	LastChangedAt time.Time
	ExportedAt    time.Time
	FinalizedAt   time.Time
	LastRunID     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Remote        Remote
}

// Remote 是分区在对象存储里的副本。Key 相对对象存储的 prefix；SHA256 等于分区的 FileSHA256 且 VerifiedAt 非零时，远端是当前版本。
type Remote struct {
	Key        string
	SHA256     string
	ETag       string
	UploadedAt time.Time
	VerifiedAt time.Time
	Error      string
}

// Replicated 报告分区当前版本是否已有校验过的远端副本（worker 与 mgr 用同一个口径）。
func (p Partition) Replicated() bool {
	return p.FilePath != "" && p.Remote.SHA256 == p.FileSHA256 && !p.Remote.VerifiedAt.IsZero()
}

const partitionColumns = `c_space_id, c_dataset_id, c_partition_key, c_range_start, c_range_end, c_status, c_row_count, c_subject_count,
c_min_data_time, c_max_data_time, c_file_path, c_file_bytes, c_file_sha256, c_columns_json, c_preview, c_export_count,
c_last_changed_at, c_exported_at, c_finalized_at, c_last_run_id,
c_remote_key, c_remote_sha256, c_remote_etag, c_remote_uploaded_at, c_remote_verified_at, c_remote_error, c_ctime, c_mtime`

func scanPartition(row rowScanner) (Partition, error) {
	var p Partition
	var rangeStart, rangeEnd, minTime, maxTime, changed, exported, finalized, uploaded, verified, created, updated timeValue
	var columns string
	err := row.Scan(&p.SpaceID, &p.DatasetID, &p.PartitionKey, &rangeStart, &rangeEnd, &p.Status, &p.RowCount, &p.SubjectCount,
		&minTime, &maxTime, &p.FilePath, &p.FileBytes, &p.FileSHA256, &columns, &p.Preview, &p.ExportCount,
		&changed, &exported, &finalized, &p.LastRunID,
		&p.Remote.Key, &p.Remote.SHA256, &p.Remote.ETag, &uploaded, &verified, &p.Remote.Error, &created, &updated)
	if err != nil {
		return Partition{}, err
	}
	p.Remote.UploadedAt, p.Remote.VerifiedAt = uploaded.Time, verified.Time
	p.RangeStart, p.RangeEnd, p.MinDataTime, p.MaxDataTime = rangeStart.Time, rangeEnd.Time, minTime.Time, maxTime.Time
	p.LastChangedAt, p.ExportedAt, p.FinalizedAt, p.CreatedAt, p.UpdatedAt = changed.Time, exported.Time, finalized.Time, created.Time, updated.Time
	if columns != "" {
		_ = json.Unmarshal([]byte(columns), &p.Columns)
	}
	return p, nil
}

// ListPartitions 按分区键倒序分页列出空间的分区；datasetID 为空表示全部数据集，fromKey、toKey 是分区键（含）的字典序范围。
func (s *Store) ListPartitions(ctx context.Context, spaceID, datasetID, fromKey, toKey string, limit, offset int) ([]Partition, int, error) {
	args := []any{spaceID, datasetID, datasetID, fromKey, fromKey, toKey, toKey}
	var total int
	if err := s.db.QueryRowContext(ctx, queryPartitionsCount, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, queryPartitionsList, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Partition
	for rows.Next() {
		p, err := scanPartition(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// TaskPartitions 按分区键升序返回一个任务下发给 worker 的分区登记：结束时间晚于 since 的（since 为零表示全部）。
// withUnreplicated 为真（启用了对象存储）时，再加上更早但有文件、还没有已校验远端副本的：暂存库重建后它们还要补传；
// 已复制的旧分区不下发。没启用对象存储时所有分区都“未复制”，不加这一条，下发量才不随年份增长。
func (s *Store) TaskPartitions(ctx context.Context, spaceID, datasetID string, since time.Time, withUnreplicated bool) ([]Partition, error) {
	query := queryPartitions5
	if withUnreplicated {
		query = queryPartitions6
	}
	ms := millisOf(since)
	rows, err := s.db.QueryContext(ctx, query, spaceID, datasetID, ms, ms)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Partition
	for rows.Next() {
		p, err := scanPartition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPartition 读取一个分区登记。
func (s *Store) GetPartition(ctx context.Context, spaceID, datasetID, key string) (Partition, error) {
	p, err := scanPartition(s.db.QueryRowContext(ctx, queryPartitions2, spaceID, datasetID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Partition{}, ErrNotFound
	}
	return p, err
}

// PartitionSummary 是任务的分区汇总。
type PartitionSummary struct {
	Partitions  int
	Files       int
	Rows        uint64
	FileBytes   uint64
	MinDataTime time.Time
	MaxDataTime time.Time
}

// PartitionSummaries 汇总每个任务的分区，用于任务列表；key 为 "space/dataset"。
func (s *Store) PartitionSummaries(ctx context.Context, spaceID string) (map[string]PartitionSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c_space_id, c_dataset_id, COUNT(*), COALESCE(SUM(CASE WHEN c_file_path != '' THEN 1 ELSE 0 END), 0),
COALESCE(SUM(c_row_count), 0), COALESCE(SUM(c_file_bytes), 0), MIN(NULLIF(c_min_data_time, 0)), MAX(NULLIF(c_max_data_time, 0))
FROM t_archive_partitions WHERE (? = '' OR c_space_id = ?) GROUP BY c_space_id, c_dataset_id`, spaceID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PartitionSummary{}
	for rows.Next() {
		var space, dataset string
		var sum PartitionSummary
		var minTime, maxTime timeValue
		if err := rows.Scan(&space, &dataset, &sum.Partitions, &sum.Files, &sum.Rows, &sum.FileBytes, &minTime, &maxTime); err != nil {
			return nil, err
		}
		sum.MinDataTime, sum.MaxDataTime = minTime.Time, maxTime.Time
		out[space+"/"+dataset] = sum
	}
	return out, rows.Err()
}

// CompleteRequest 是 worker 上报的手动、补拉区间或导入运行结果。
type CompleteRequest struct {
	RunID         string
	LeaseToken    string
	Status        string
	Message       string
	Stats         RunStats
	Unrecoverable []Unrecoverable
	Partitions    []Partition
	FinalUntil    time.Time
	RescanFrom    time.Time
	RescanUntil   time.Time
	Now           time.Time
}

// CompleteRun 在一个事务里更新运行状态、分区登记、任务的最近成功时间、整段重读进度与定稿边界。
// 运行已被用户取消（cancelled）时仍接受统计与分区登记，状态保持 cancelled；已处于其他终态的运行返回 ErrConflict。
// 上报里有分区的行数或标的数比已登记的少（已有文件时）就整次拒绝：运行记为失败，不更新分区与任务，返回 ErrPartitionRegression。
func (s *Store) CompleteRun(ctx context.Context, req CompleteRequest) error {
	if req.Status != RunSucceeded && req.Status != RunFailed && req.Status != RunCancelled {
		return fmt.Errorf("invalid completion status %q", req.Status)
	}
	if req.RescanFrom.IsZero() != req.RescanUntil.IsZero() {
		return errors.New("rescan_from 与 rescan_until 必须同时为空或同时非空")
	}
	var regression error
	err := s.tx(ctx, func(tx *sql.Tx) error {
		run, err := scanRun(tx.QueryRowContext(ctx, queryPartitions3, req.RunID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if run.LeaseToken != req.LeaseToken {
			return ErrConflict
		}
		if run.Status != RunRunning && run.Status != RunCancelled {
			return ErrConflict
		}
		status, message := req.Status, req.Message
		if run.Status == RunCancelled {
			status = RunCancelled
			if message == "" {
				message = run.Message
			}
		}
		reason, err := findRegression(ctx, tx, run.SpaceID, run.DatasetID, req.Partitions)
		if err != nil {
			return err
		}
		if reason != "" {
			regression = fmt.Errorf("%w：%s", ErrPartitionRegression, reason)
			// 只记下失败原因，分区与任务都不动；worker 收到冲突不清待上报标记，下次运行或跟随还会报、还会被拒，健康等级一直显示异常。
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'failed', c_finished_at = ?, c_message = ?, c_lease_until = 0, c_lease_token = '' WHERE c_run_id = ?`,
				millisOf(req.Now), reason, req.RunID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_blocked_reason = ? WHERE c_space_id = ? AND c_dataset_id = ?`, reason, run.SpaceID, run.DatasetID)
			return err
		}
		// 只有带分区的上报才清掉分区登记被拒的原因：被拒的分区一直待上报，任务绑定后的每次上报都会带上它；
		// 不带分区的上报（绑定前失败、排队时被取消）说明不了问题已解决。
		if len(req.Partitions) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_blocked_reason = '' WHERE c_space_id = ? AND c_dataset_id = ? AND c_blocked_reason != ''`,
				run.SpaceID, run.DatasetID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, completeRun, status, millisOf(req.Now), message,
			req.Stats.SubjectsDone, req.Stats.RequestCount, req.Stats.BytesRead, req.Stats.RowsRead,
			req.Stats.RowsAdded, req.Stats.RowsChanged, req.Stats.PartitionsExported, req.Stats.PartitionsFinalized, req.Stats.PartitionsUploaded,
			mergeUnrecoverable(req.Unrecoverable), req.RunID); err != nil {
			return err
		}
		for _, partition := range req.Partitions {
			partition.SpaceID, partition.DatasetID, partition.LastRunID = run.SpaceID, run.DatasetID, req.RunID
			if err := upsertPartition(ctx, tx, partition); err != nil {
				return err
			}
		}
		// 被取消的运行即使 worker 上报成功也不推进任务状态；manual 成功时同步推进最近跟随与重读进度。
		// c_last_success_at 记录成功完成时刻（与 c_last_follow_at 一致），避免长时间手动运行结束后触发“距今超过 30 分钟”的健康误报。
		if status == RunSucceeded && run.Kind == KindManual {
			started := millisOf(run.StartedAt)
			finished := millisOf(req.Now)
			if finished == 0 {
				finished = started
			}
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks
SET c_last_follow_at = CASE WHEN c_last_follow_at < ? THEN ? ELSE c_last_follow_at END,
    c_last_success_at = CASE WHEN c_last_success_at = 0 OR c_last_success_at < ? THEN ? ELSE c_last_success_at END,
    c_last_follow_error = '',
    c_last_follow_message = ?,
    c_rescan_from = ?,
    c_rescan_until = ?
WHERE c_space_id = ? AND c_dataset_id = ?`,
				finished, finished, finished, finished, message, millisOf(req.RescanFrom), millisOf(req.RescanUntil), run.SpaceID, run.DatasetID); err != nil {
				return err
			}
		}
		if len(req.Unrecoverable) > 0 {
			if err := accumulateUnrecoverable(ctx, tx, run.SpaceID, run.DatasetID, req.Unrecoverable); err != nil {
				return err
			}
		}
		if status == RunSucceeded && !req.FinalUntil.IsZero() {
			final := millisOf(req.FinalUntil)
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_final_until = ? WHERE c_space_id = ? AND c_dataset_id = ? AND (c_final_until = 0 OR c_final_until < ?)`,
				final, run.SpaceID, run.DatasetID, final); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return regression
}

// FollowReport 是 worker 上报一轮本地跟随结果的参数。
type FollowReport struct {
	SpaceID       string
	DatasetID     string
	TaskID        int64
	Status        string
	StartedAt     time.Time
	FinishedAt    time.Time
	Message       string
	Unrecoverable []Unrecoverable
	Partitions    []Partition
	FinalUntil    time.Time
	RescanFrom    time.Time
	RescanUntil   time.Time
}

// ReportFollow 在一个事务里按任务 ID 校验并写入本地跟随结果（成功时刻、错误、分区登记、定稿边界与整段重读进度）。
// 任务不存在返回 ErrNotFound；任务已删除重建导致 TaskID 不符返回 ErrConflict；分区倒退记入阻塞原因并返回 ErrPartitionRegression。
func (s *Store) ReportFollow(ctx context.Context, req FollowReport) error {
	if req.Status != RunSucceeded && req.Status != RunFailed && req.Status != RunCancelled {
		return fmt.Errorf("invalid follow status %q", req.Status)
	}
	if req.RescanFrom.IsZero() != req.RescanUntil.IsZero() {
		return errors.New("rescan_from 与 rescan_until 必须同时为空或同时非空")
	}
	var regression error
	err := s.tx(ctx, func(tx *sql.Tx) error {
		task, err := scanTask(tx.QueryRowContext(ctx, queryTasks1, req.SpaceID, req.DatasetID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if task.ID != req.TaskID {
			return fmt.Errorf("%w：任务 ID 不匹配（上报 %d，当前 %d）", ErrConflict, req.TaskID, task.ID)
		}
		started := millisOf(req.StartedAt)
		finished := millisOf(req.FinishedAt)
		if finished == 0 {
			finished = started
		}
		reason, err := findRegression(ctx, tx, req.SpaceID, req.DatasetID, req.Partitions)
		if err != nil {
			return err
		}
		if reason != "" {
			regression = fmt.Errorf("%w：%s", ErrPartitionRegression, reason)
			_, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks
SET c_blocked_reason = ?,
    c_last_follow_at = CASE WHEN c_last_follow_at < ? THEN ? ELSE c_last_follow_at END,
    c_last_follow_error = ?,
    c_last_follow_message = ''
WHERE c_id = ?`, reason, finished, finished, reason, task.ID)
			return err
		}
		if len(req.Partitions) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_blocked_reason = '' WHERE c_id = ? AND c_blocked_reason != ''`, task.ID); err != nil {
				return err
			}
		}
		for _, partition := range req.Partitions {
			partition.SpaceID, partition.DatasetID, partition.LastRunID = req.SpaceID, req.DatasetID, ""
			if err := upsertPartition(ctx, tx, partition); err != nil {
				return err
			}
		}
		switch req.Status {
		case RunSucceeded:
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks
SET c_last_follow_at = CASE WHEN c_last_follow_at < ? THEN ? ELSE c_last_follow_at END,
    c_last_success_at = CASE WHEN c_last_success_at = 0 OR c_last_success_at < ? THEN ? ELSE c_last_success_at END,
    c_last_follow_error = '',
    c_last_follow_message = ?,
    c_rescan_from = ?,
    c_rescan_until = ?
WHERE c_id = ?`,
				finished, finished, finished, finished, req.Message, millisOf(req.RescanFrom), millisOf(req.RescanUntil), task.ID); err != nil {
				return err
			}
		case RunFailed:
			errMsg := strings.TrimSpace(req.Message)
			if errMsg == "" {
				errMsg = "跟随失败"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks
SET c_last_follow_at = CASE WHEN c_last_follow_at < ? THEN ? ELSE c_last_follow_at END,
    c_last_follow_error = ?,
    c_last_follow_message = ''
WHERE c_id = ?`,
				finished, finished, errMsg, task.ID); err != nil {
				return err
			}
		case RunCancelled:
			// 被取消的跟随只登记已导出分区，不推进成功时间也不覆盖失败信息或重读进度。
		}
		if len(req.Unrecoverable) > 0 {
			if err := accumulateUnrecoverable(ctx, tx, req.SpaceID, req.DatasetID, req.Unrecoverable); err != nil {
				return err
			}
		}
		if req.Status == RunSucceeded && !req.FinalUntil.IsZero() {
			final := millisOf(req.FinalUntil)
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_final_until = ? WHERE c_id = ? AND (c_final_until = 0 OR c_final_until < ?)`,
				final, task.ID, final); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return regression
}

// ErrPartitionRegression 表示上报的分区行数或标的数比已登记的少。归档只增不删（读回合并后不会变少），
// 变少只会来自暂存状态丢失后用残缺数据重新导出，必须拒绝。它包含 ErrConflict，调用方按冲突处理。
var ErrPartitionRegression = fmt.Errorf("%w: partition regression", ErrConflict)

// findRegression 检查上报的分区里有没有比已登记的（已有文件的）行数或标的数少的，有则返回原因。
func findRegression(ctx context.Context, tx *sql.Tx, spaceID, datasetID string, reports []Partition) (string, error) {
	for _, p := range reports {
		existing, err := scanPartition(tx.QueryRowContext(ctx, queryPartitions2, spaceID, datasetID, p.PartitionKey))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", err
		}
		if existing.FilePath == "" {
			continue
		}
		if p.RowCount < existing.RowCount || p.SubjectCount < existing.SubjectCount {
			return fmt.Sprintf("分区 %s 的行数从 %d 降到 %d、标的数从 %d 降到 %d，拒绝登记；可能是暂存库丢失后用残缺数据重新导出，原文件需要人工核对",
				p.PartitionKey, existing.RowCount, p.RowCount, existing.SubjectCount, p.SubjectCount), nil
		}
	}
	return "", nil
}

// accumulateUnrecoverable 把本次的无法补齐区间并进任务的累积记录（按同标的同序列归并重叠/相邻区间，最多 maxUnrecoverable 条）。
func accumulateUnrecoverable(ctx context.Context, tx *sql.Tx, spaceID, datasetID string, items []Unrecoverable) error {
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT c_unrecoverable_json FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID).Scan(&current); err != nil {
		return err
	}
	var merged []Unrecoverable
	if current != "" && current != "[]" {
		_ = json.Unmarshal([]byte(current), &merged)
	}
	merged = append(merged, items...)
	_, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_unrecoverable_json = ? WHERE c_space_id = ? AND c_dataset_id = ?`, mergeUnrecoverable(merged), spaceID, datasetID)
	return err
}

func upsertPartition(ctx context.Context, tx *sql.Tx, p Partition) error {
	if p.PartitionKey == "" {
		return errors.New("partition key is required")
	}
	if p.Status != PartitionOpen && p.Status != PartitionExported && p.Status != PartitionFinal {
		return fmt.Errorf("invalid partition status %q", p.Status)
	}
	existing, err := scanPartition(tx.QueryRowContext(ctx, queryPartitions2,
		p.SpaceID, p.DatasetID, p.PartitionKey))
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// 已有文件的分区被补写时，worker 会先读回原文件、标回 open 再重新导出：open 状态不带文件信息时沿用已登记的文件，
	// 远端副本一并沿用（没有文件就谈不上远端副本，沿用旧值才不会把已上传的记录冲掉）。
	if found && p.FilePath == "" {
		p.FilePath, p.FileBytes, p.FileSHA256, p.Remote = existing.FilePath, existing.FileBytes, existing.FileSHA256, existing.Remote
		if len(p.Columns) == 0 {
			p.Columns = existing.Columns
		}
		if p.Preview == "" {
			p.Preview = existing.Preview
		}
		if p.ExportedAt.IsZero() {
			p.ExportedAt = existing.ExportedAt
		}
		if p.ExportCount == 0 {
			p.ExportCount = existing.ExportCount
		}
	}
	// 文件大小兜底：worker 上报的大小是上报时 stat 的，本地文件不在时为 0（按登记恢复、本地缺文件的分区）。
	// 路径与 sha256 都没变时保留已登记的大小与预览（本次没有重新导出时 worker 不带预览），不冲成空。
	if found && p.FilePath == existing.FilePath && p.FileSHA256 == existing.FileSHA256 {
		if p.FileBytes == 0 {
			p.FileBytes = existing.FileBytes
		}
		if p.Preview == "" {
			p.Preview = existing.Preview
		}
	}
	if p.Status != PartitionOpen && p.FilePath == "" {
		return fmt.Errorf("partition %s is %s but has no file path", p.PartitionKey, p.Status)
	}
	if p.Remote.Key != "" && p.Remote.SHA256 == "" {
		return fmt.Errorf("分区 %s 的远端副本有对象键却没有 sha256", p.PartitionKey)
	}
	if p.Status != PartitionFinal {
		p.FinalizedAt = time.Time{}
	}
	columns := "[]"
	if len(p.Columns) > 0 {
		raw, err := json.Marshal(p.Columns)
		if err != nil {
			return err
		}
		columns = string(raw)
	}
	if !found {
		_, err = tx.ExecContext(ctx, insertPartition,
			p.SpaceID, p.DatasetID, p.PartitionKey, millisOf(p.RangeStart), millisOf(p.RangeEnd), p.Status,
			p.RowCount, p.SubjectCount, millisOf(p.MinDataTime), millisOf(p.MaxDataTime), p.FilePath, p.FileBytes, p.FileSHA256, columns, p.Preview, p.ExportCount,
			millisOf(p.LastChangedAt), millisOf(p.ExportedAt), millisOf(p.FinalizedAt), p.LastRunID,
			p.Remote.Key, p.Remote.SHA256, p.Remote.ETag, millisOf(p.Remote.UploadedAt), millisOf(p.Remote.VerifiedAt), p.Remote.Error)
		return err
	}
	_, err = tx.ExecContext(ctx, updatePartition,
		millisOf(p.RangeStart), millisOf(p.RangeEnd), p.Status, p.RowCount, p.SubjectCount, millisOf(p.MinDataTime), millisOf(p.MaxDataTime),
		p.FilePath, p.FileBytes, p.FileSHA256, columns, p.Preview, p.ExportCount, millisOf(p.LastChangedAt), millisOf(p.ExportedAt),
		millisOf(p.FinalizedAt), p.LastRunID,
		p.Remote.Key, p.Remote.SHA256, p.Remote.ETag, millisOf(p.Remote.UploadedAt), millisOf(p.Remote.VerifiedAt), p.Remote.Error,
		p.SpaceID, p.DatasetID, p.PartitionKey)
	return err
}

// RemoteBacklog 是一个任务还没有已校验远端副本的分区（有文件的）汇总，用于健康等级。
type RemoteBacklog struct {
	Unreplicated int
	// OldestExportedAt 与 OldestKey 是其中导出时间最早的那个分区。
	OldestExportedAt time.Time
	OldestKey        string
	// LastError 是该任务最近一次更新（按 c_mtime）的远端错误；ErrorKey 是它所在的分区。
	LastError string
	ErrorKey  string
}

// RemoteBacklogs 按任务（键 "space/dataset"）汇总未复制的分区与最近的远端错误；spaceID 为空表示全部空间。
func (s *Store) RemoteBacklogs(ctx context.Context, spaceID string) (map[string]RemoteBacklog, error) {
	out := map[string]RemoteBacklog{}
	rows, err := s.db.QueryContext(ctx, queryRemoteBacklog, spaceID, spaceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var space, dataset string
		var b RemoteBacklog
		var oldest timeValue
		if err := rows.Scan(&space, &dataset, &b.Unreplicated, &oldest); err != nil {
			_ = rows.Close()
			return nil, err
		}
		b.OldestExportedAt = oldest.Time
		out[space+"/"+dataset] = b
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for key, b := range out {
		space, dataset, _ := strings.Cut(key, "/")
		if err := s.db.QueryRowContext(ctx, queryRemoteOldest, space, dataset).Scan(&b.OldestKey); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		out[key] = b
	}
	rows, err = s.db.QueryContext(ctx, queryRemoteLastError, spaceID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var space, dataset, key, text string
		if err := rows.Scan(&space, &dataset, &key, &text); err != nil {
			return nil, err
		}
		// 同一毫秒内可能有几条并列：取分区键最小的那条，结果稳定。
		b := out[space+"/"+dataset]
		if b.LastError == "" || key < b.ErrorKey {
			b.LastError, b.ErrorKey = text, key
		}
		out[space+"/"+dataset] = b
	}
	return out, rows.Err()
}

// ListOpenPartitions 返回尚未导出（open）的分区登记，用于任务健康等级。
func (s *Store) ListOpenPartitions(ctx context.Context, spaceID string) ([]Partition, error) {
	rows, err := s.db.QueryContext(ctx, queryPartitions4, spaceID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Partition
	for rows.Next() {
		p, err := scanPartition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// 以下查询语句整条写成常量，避免在调用处拼接 SQL。
const (
	queryPartitions2 = `SELECT ` + partitionColumns + ` FROM t_archive_partitions WHERE c_space_id = ? AND c_dataset_id = ? AND c_partition_key = ?`
	queryPartitions3 = `SELECT ` + runColumns + ` FROM t_archive_runs WHERE c_run_id = ?`
	queryPartitions4 = `SELECT ` + partitionColumns + ` FROM t_archive_partitions WHERE c_status = 'open' AND (? = '' OR c_space_id = ?)`
	queryPartitions5 = `SELECT ` + partitionColumns + ` FROM t_archive_partitions WHERE c_space_id = ? AND c_dataset_id = ? AND (? = 0 OR c_range_end > ?) ORDER BY c_partition_key`
	queryPartitions6 = `SELECT ` + partitionColumns + ` FROM t_archive_partitions WHERE c_space_id = ? AND c_dataset_id = ?
AND ((? = 0 OR c_range_end > ?) OR (` + unreplicatedFilter + `))
ORDER BY c_partition_key`
	completeRun = `UPDATE t_archive_runs SET c_status = ?, c_finished_at = ?, c_message = ?, c_subjects_done = ?,
c_request_count = ?, c_bytes_read = ?, c_rows_read = ?, c_rows_added = ?, c_rows_changed = ?, c_partitions_exported = ?, c_partitions_finalized = ?,
c_partitions_uploaded = ?, c_unrecoverable_json = ?, c_lease_until = 0, c_lease_token = '' WHERE c_run_id = ?`
	insertPartition = `INSERT INTO t_archive_partitions (c_space_id, c_dataset_id, c_partition_key, c_range_start, c_range_end, c_status,
c_row_count, c_subject_count, c_min_data_time, c_max_data_time, c_file_path, c_file_bytes, c_file_sha256, c_columns_json, c_preview, c_export_count,
c_last_changed_at, c_exported_at, c_finalized_at, c_last_run_id,
c_remote_key, c_remote_sha256, c_remote_etag, c_remote_uploaded_at, c_remote_verified_at, c_remote_error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	updatePartition = `UPDATE t_archive_partitions SET c_range_start = ?, c_range_end = ?, c_status = ?, c_row_count = ?, c_subject_count = ?,
c_min_data_time = ?, c_max_data_time = ?, c_file_path = ?, c_file_bytes = ?, c_file_sha256 = ?, c_columns_json = ?, c_preview = ?, c_export_count = ?,
c_last_changed_at = ?, c_exported_at = ?, c_finalized_at = ?, c_last_run_id = ?,
c_remote_key = ?, c_remote_sha256 = ?, c_remote_etag = ?, c_remote_uploaded_at = ?, c_remote_verified_at = ?, c_remote_error = ?
WHERE c_space_id = ? AND c_dataset_id = ? AND c_partition_key = ?`
)

// 远端副本积压：未复制 = 有文件且 NOT (远端 sha256 = 文件 sha256 且已校验)。
const (
	unreplicatedFilter = `c_file_path != '' AND NOT (c_remote_sha256 = c_file_sha256 AND c_remote_verified_at > 0)`
	queryRemoteBacklog = `SELECT c_space_id, c_dataset_id, COUNT(*), MIN(NULLIF(c_exported_at, 0)) FROM t_archive_partitions
WHERE (? = '' OR c_space_id = ?) AND ` + unreplicatedFilter + ` GROUP BY c_space_id, c_dataset_id`
	queryRemoteOldest = `SELECT c_partition_key FROM t_archive_partitions WHERE c_space_id = ? AND c_dataset_id = ? AND ` + unreplicatedFilter + `
ORDER BY CASE WHEN c_exported_at = 0 THEN 1 ELSE 0 END, c_exported_at, c_partition_key LIMIT 1`
	// 每个任务最近一次更新的那条错误（按 c_mtime，不按字典序取 MAX）。
	queryRemoteLastError = `SELECT c_space_id, c_dataset_id, c_partition_key, c_remote_error FROM t_archive_partitions p
WHERE (? = '' OR c_space_id = ?) AND c_remote_error != ''
AND c_mtime = (SELECT MAX(q.c_mtime) FROM t_archive_partitions q
WHERE q.c_space_id = p.c_space_id AND q.c_dataset_id = p.c_dataset_id AND q.c_remote_error != '')`
)

// 分区列表的筛选条件固定，datasetID、fromKey、toKey 为空表示不限。
const (
	partitionsFilter     = `c_space_id = ? AND (? = '' OR c_dataset_id = ?) AND (? = '' OR c_partition_key >= ?) AND (? = '' OR c_partition_key <= ?)`
	queryPartitionsCount = `SELECT COUNT(*) FROM t_archive_partitions WHERE ` + partitionsFilter
	queryPartitionsList  = `SELECT ` + partitionColumns + ` FROM t_archive_partitions WHERE ` + partitionsFilter + ` ORDER BY c_dataset_id, c_partition_key DESC LIMIT ? OFFSET ?`
)
