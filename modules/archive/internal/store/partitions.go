package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	WorkerID      string
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
}

const partitionColumns = `c_space_id, c_dataset_id, c_partition_key, c_range_start, c_range_end, c_status, c_worker_id, c_row_count, c_subject_count,
c_min_data_time, c_max_data_time, c_file_path, c_file_bytes, c_file_sha256, c_columns_json, c_preview, c_export_count,
c_last_changed_at, c_exported_at, c_finalized_at, c_last_run_id, c_ctime, c_mtime`

func scanPartition(row rowScanner) (Partition, error) {
	var p Partition
	var rangeStart, rangeEnd, minTime, maxTime, changed, exported, finalized, created, updated timeValue
	var columns string
	err := row.Scan(&p.SpaceID, &p.DatasetID, &p.PartitionKey, &rangeStart, &rangeEnd, &p.Status, &p.WorkerID, &p.RowCount, &p.SubjectCount,
		&minTime, &maxTime, &p.FilePath, &p.FileBytes, &p.FileSHA256, &columns, &p.Preview, &p.ExportCount,
		&changed, &exported, &finalized, &p.LastRunID, &created, &updated)
	if err != nil {
		return Partition{}, err
	}
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

// TaskPartitions 按分区键升序返回一个任务结束时间晚于 since 的分区登记（since 为零表示全部）。
func (s *Store) TaskPartitions(ctx context.Context, spaceID, datasetID string, since time.Time) ([]Partition, error) {
	rows, err := s.db.QueryContext(ctx, queryPartitions5, spaceID, datasetID, fmtTime(since))
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
COALESCE(SUM(c_row_count), 0), COALESCE(SUM(c_file_bytes), 0), MIN(c_min_data_time), MAX(c_max_data_time)
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

// CompleteRequest 是 worker 上报的运行结果。
type CompleteRequest struct {
	RunID         string
	WorkerID      string
	LeaseToken    string
	Status        string
	Message       string
	Stats         RunStats
	Unrecoverable []Unrecoverable
	Partitions    []Partition
	FinalUntil    time.Time
	Now           time.Time
}

// CompleteRun 在一个事务里更新运行状态、分区登记、任务的最近成功时间与定稿边界。
// 运行已被用户取消（canceled）时仍接受统计与分区登记，状态保持 canceled；已处于其他终态的运行返回 ErrConflict。
// 上报里有分区的行数或标的数比已登记的少（已有文件时）就整次拒绝：运行记为失败，不更新分区与任务，返回 ErrPartitionRegression。
func (s *Store) CompleteRun(ctx context.Context, req CompleteRequest) error {
	if req.Status != RunSucceeded && req.Status != RunFailed && req.Status != RunCanceled {
		return fmt.Errorf("invalid completion status %q", req.Status)
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
		if run.LeaseToken != req.LeaseToken || run.WorkerID != req.WorkerID {
			return ErrConflict
		}
		if run.Status != RunRunning && run.Status != RunCanceled {
			return ErrConflict
		}
		status, message := req.Status, req.Message
		if run.Status == RunCanceled {
			status = RunCanceled
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
			// 只记下失败原因，分区与任务都不动；worker 收到冲突不清待上报标记，下次运行还会报、还会被拒，健康等级一直显示失败。
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = 'failed', c_finished_at = ?, c_message = ?, c_lease_until = NULL, c_lease_token = '' WHERE c_run_id = ?`,
				fmtTime(req.Now), reason, req.RunID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_blocked_reason = ? WHERE c_space_id = ? AND c_dataset_id = ?`, reason, run.SpaceID, run.DatasetID)
			return err
		}
		// 只有带分区的上报才清掉被拒原因：被拒的分区一直待上报，任务绑定后的每次上报都会带上它；
		// 不带分区的上报（绑定前失败、排队时被取消）说明不了问题已解决。
		if len(req.Partitions) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_blocked_reason = '' WHERE c_space_id = ? AND c_dataset_id = ? AND c_blocked_reason != ''`, run.SpaceID, run.DatasetID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE t_archive_runs SET c_status = ?, c_finished_at = ?, c_message = ?, c_subjects_total = ?, c_subjects_done = ?,
c_request_count = ?, c_bytes_read = ?, c_rows_read = ?, c_rows_added = ?, c_rows_changed = ?, c_partitions_exported = ?, c_partitions_finalized = ?,
c_unrecoverable_json = ?, c_lease_until = NULL, c_lease_token = '' WHERE c_run_id = ?`, status, fmtTime(req.Now), message,
			req.Stats.SubjectsTotal, req.Stats.SubjectsDone, req.Stats.RequestCount, req.Stats.BytesRead, req.Stats.RowsRead,
			req.Stats.RowsAdded, req.Stats.RowsChanged, req.Stats.PartitionsExported, req.Stats.PartitionsFinalized,
			mergeUnrecoverable(req.Unrecoverable), req.RunID); err != nil {
			return err
		}
		for _, partition := range req.Partitions {
			partition.SpaceID, partition.DatasetID, partition.WorkerID, partition.LastRunID = run.SpaceID, run.DatasetID, req.WorkerID, req.RunID
			if err := upsertPartition(ctx, tx, partition); err != nil {
				return err
			}
		}
		// 被取消的运行即使 worker 上报成功也不推进任务状态；最近成功时间只前进不回退。
		// 水位已推进，回补起点不再需要：清掉它，免得暂存库丢失后又从回补起点整段重读。
		if status == RunSucceeded && run.Kind != KindRange {
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_last_success_at = ?, c_backfill_from = NULL WHERE c_space_id = ? AND c_dataset_id = ? AND (c_last_success_at IS NULL OR c_last_success_at < ?)`,
				fmtTime(run.StartedAt), run.SpaceID, run.DatasetID, fmtTime(run.StartedAt)); err != nil {
				return err
			}
		}
		if len(req.Unrecoverable) > 0 {
			if err := accumulateUnrecoverable(ctx, tx, run.SpaceID, run.DatasetID, req.Unrecoverable); err != nil {
				return err
			}
		}
		if status == RunSucceeded && !req.FinalUntil.IsZero() {
			if _, err := tx.ExecContext(ctx, `UPDATE t_archive_tasks SET c_final_until = ? WHERE c_space_id = ? AND c_dataset_id = ? AND (c_final_until IS NULL OR c_final_until < ?)`,
				fmtTime(req.FinalUntil), run.SpaceID, run.DatasetID, fmtTime(req.FinalUntil)); err != nil {
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

// accumulateUnrecoverable 把本次的无法补齐区间并进任务的累积记录（去重，最多 maxUnrecoverable 条）。
func accumulateUnrecoverable(ctx context.Context, tx *sql.Tx, spaceID, datasetID string, items []Unrecoverable) error {
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT c_unrecoverable_json FROM t_archive_tasks WHERE c_space_id = ? AND c_dataset_id = ?`, spaceID, datasetID).Scan(&current); err != nil {
		return err
	}
	var merged []Unrecoverable
	if current != "" && current != "[]" {
		_ = json.Unmarshal([]byte(current), &merged)
	}
	seen := map[Unrecoverable]bool{}
	for _, item := range merged {
		seen[item] = true
	}
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			merged = append(merged, item)
		}
	}
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
	// 一个分区只有一份文件、一条登记：登记的 worker 不同则拒绝，避免数据分散在两台机器上。
	if found && existing.WorkerID != p.WorkerID {
		return fmt.Errorf("%w: partition %s belongs to worker %s", ErrConflict, p.PartitionKey, existing.WorkerID)
	}
	// 已有文件的分区被补写时，worker 会先读回原文件、标回 open 再重新导出：open 状态不带文件信息时沿用已登记的文件。
	if found && p.FilePath == "" {
		p.FilePath, p.FileBytes, p.FileSHA256 = existing.FilePath, existing.FileBytes, existing.FileSHA256
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
	if p.Status != PartitionOpen && p.FilePath == "" {
		return fmt.Errorf("partition %s is %s but has no file path", p.PartitionKey, p.Status)
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
		_, err = tx.ExecContext(ctx, `INSERT INTO t_archive_partitions (c_space_id, c_dataset_id, c_partition_key, c_range_start, c_range_end, c_status, c_worker_id,
c_row_count, c_subject_count, c_min_data_time, c_max_data_time, c_file_path, c_file_bytes, c_file_sha256, c_columns_json, c_preview, c_export_count,
c_last_changed_at, c_exported_at, c_finalized_at, c_last_run_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.SpaceID, p.DatasetID, p.PartitionKey, fmtTime(p.RangeStart), fmtTime(p.RangeEnd), p.Status, p.WorkerID,
			p.RowCount, p.SubjectCount, nullTime(p.MinDataTime), nullTime(p.MaxDataTime), p.FilePath, p.FileBytes, p.FileSHA256, columns, p.Preview, p.ExportCount,
			nullTime(p.LastChangedAt), nullTime(p.ExportedAt), nullTime(p.FinalizedAt), p.LastRunID)
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE t_archive_partitions SET c_range_start = ?, c_range_end = ?, c_status = ?, c_row_count = ?, c_subject_count = ?,
c_min_data_time = ?, c_max_data_time = ?, c_file_path = ?, c_file_bytes = ?, c_file_sha256 = ?, c_columns_json = ?, c_preview = ?, c_export_count = ?,
c_last_changed_at = ?, c_exported_at = ?, c_finalized_at = ?, c_last_run_id = ? WHERE c_space_id = ? AND c_dataset_id = ? AND c_partition_key = ?`,
		fmtTime(p.RangeStart), fmtTime(p.RangeEnd), p.Status, p.RowCount, p.SubjectCount, nullTime(p.MinDataTime), nullTime(p.MaxDataTime),
		p.FilePath, p.FileBytes, p.FileSHA256, columns, p.Preview, p.ExportCount, nullTime(p.LastChangedAt), nullTime(p.ExportedAt),
		nullTime(p.FinalizedAt), p.LastRunID, p.SpaceID, p.DatasetID, p.PartitionKey)
	return err
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
	queryPartitions5 = `SELECT ` + partitionColumns + ` FROM t_archive_partitions WHERE c_space_id = ? AND c_dataset_id = ? AND c_range_end > ? ORDER BY c_partition_key`
)

// 分区列表的筛选条件固定，datasetID、fromKey、toKey 为空表示不限。
const (
	partitionsFilter     = `c_space_id = ? AND (? = '' OR c_dataset_id = ?) AND (? = '' OR c_partition_key >= ?) AND (? = '' OR c_partition_key <= ?)`
	queryPartitionsCount = `SELECT COUNT(*) FROM t_archive_partitions WHERE ` + partitionsFilter
	queryPartitionsList  = `SELECT ` + partitionColumns + ` FROM t_archive_partitions WHERE ` + partitionsFilter + ` ORDER BY c_dataset_id, c_partition_key DESC LIMIT ? OFFSET ?`
)
