package staging

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// PartitionState 是一个分区的状态，永久保留（定稿的分区被补写时据此判断要先读回原文件）。
type PartitionState struct {
	Key           string
	Start         time.Time
	End           time.Time
	Status        string
	LastChangedAt time.Time
	ExportedAt    time.Time
	// FilePath 是导出文件相对归档根目录的路径（导出时记下；读回、上报、判断文件缺失都用它）。
	FilePath    string
	SHA256      string
	ExportCount uint32
	// 定稿时记下的统计（定稿后暂存行已删除，登记要靠它）；未定稿的分区用 Stats 现算。
	RowCount     uint64
	SubjectCount uint64
	MinDataTime  time.Time
	MaxDataTime  time.Time
	FinalizedAt  time.Time
	// ReportPending 为真表示分区的最新状态还没有被 mgr 确认登记。
	ReportPending bool
	// Remote 是分区在对象存储里的副本。
	Remote Remote
}

// Remote 是分区的远端副本状态。Key 相对对象存储的 prefix；Attempts 与 RetryAt 是上传失败后的退避，只在本地。
type Remote struct {
	Key        string
	SHA256     string
	ETag       string
	UploadedAt time.Time
	VerifiedAt time.Time
	Error      string
	Attempts   int
	RetryAt    time.Time
}

// Replicated 报告当前版本是否已有校验过的远端副本（与 mgr 同一个口径）。
func (p PartitionState) Replicated() bool {
	return p.FilePath != "" && p.Remote.SHA256 == p.SHA256 && !p.Remote.VerifiedAt.IsZero()
}

// RemoteIsCurrent 报告远端有当前版本的对象（读回时可以从它取回）。
func (p PartitionState) RemoteIsCurrent() bool {
	return p.Remote.Key != "" && p.SHA256 != "" && p.Remote.SHA256 == p.SHA256
}

// NeedsExport 报告分区是否需要导出：从未导出，或导出之后又有变化。
func (p PartitionState) NeedsExport() bool {
	if p.Status == StatusFinal {
		return false
	}
	return p.ExportedAt.IsZero() || p.LastChangedAt.After(p.ExportedAt)
}

const partitionColumns = `partition_key, range_start, range_end, status, last_changed_at, exported_at, file_path, file_sha256, export_count, row_count, subject_count, min_data_time, max_data_time, finalized_at, report_pending,
remote_key, remote_sha256, remote_etag, remote_uploaded_at, remote_verified_at, remote_error, remote_attempts, remote_retry_at`

func scanPartition(row interface{ Scan(...any) error }) (PartitionState, error) {
	p, _, _, err := scanPartitionWith(row, false)
	return p, err
}

// scanPartitionWith 扫描 partitionColumns；withTask 为真时前面多两列 space_id、dataset_id。
func scanPartitionWith(row interface{ Scan(...any) error }, withTask bool) (PartitionState, string, string, error) {
	var p PartitionState
	var space, dataset string
	var start, end, changed, rows, subjects, pending int64
	var exported, minTime, maxTime, finalized, uploaded, verified, retry sql.NullInt64
	targets := []any{&p.Key, &start, &end, &p.Status, &changed, &exported, &p.FilePath, &p.SHA256, &p.ExportCount, &rows, &subjects, &minTime, &maxTime, &finalized, &pending,
		&p.Remote.Key, &p.Remote.SHA256, &p.Remote.ETag, &uploaded, &verified, &p.Remote.Error, &p.Remote.Attempts, &retry}
	if withTask {
		targets = append([]any{&space, &dataset}, targets...)
	}
	if err := row.Scan(targets...); err != nil {
		return PartitionState{}, "", "", err
	}
	p.Start, p.End, p.LastChangedAt = fromNanos(start), fromNanos(end), fromNanos(changed)
	p.ExportedAt, p.MinDataTime, p.MaxDataTime, p.FinalizedAt = scanNanos(exported), scanNanos(minTime), scanNanos(maxTime), scanNanos(finalized)
	p.RowCount, p.SubjectCount, p.ReportPending = uint64(rows), uint64(subjects), pending != 0
	p.Remote.UploadedAt, p.Remote.VerifiedAt, p.Remote.RetryAt = scanNanos(uploaded), scanNanos(verified), scanNanos(retry)
	return p, space, dataset, nil
}

// Partitions 按分区键升序返回任务的全部分区状态。
func (s *Store) Partitions(ctx context.Context, spaceID, datasetID string) ([]PartitionState, error) {
	rows, err := s.db.QueryContext(ctx, queryPartitions1, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PartitionState
	for rows.Next() {
		p, err := scanPartition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Partition 读取一个分区的状态。
func (s *Store) Partition(ctx context.Context, spaceID, datasetID, key string) (PartitionState, bool, error) {
	p, err := scanPartition(s.db.QueryRowContext(ctx, queryPartitions2, spaceID, datasetID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return PartitionState{}, false, nil
	}
	return p, err == nil, err
}

// PartitionStats 是暂存区里一个分区的行数、标的数和数据时间范围。
type PartitionStats struct {
	Rows     uint64
	Subjects uint64
	Min      time.Time
	Max      time.Time
}

// Stats 统计暂存表里一个分区的数据。
func (s *Store) Stats(ctx context.Context, task Task, p PartitionState) (PartitionStats, error) {
	var out PartitionStats
	var rows, subjects int64
	var min, max sql.NullInt64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*), count(DISTINCT subject_id), min(data_time), max(data_time) FROM %s WHERE data_time >= ? AND data_time < ?`, quote(task.Table())),
		nanos(p.Start), nanos(p.End)).Scan(&rows, &subjects, &min, &max)
	if err != nil {
		return out, err
	}
	out.Rows, out.Subjects, out.Min, out.Max = uint64(rows), uint64(subjects), scanNanos(min), scanNanos(max)
	return out, nil
}

// ExportCSV 把分区的暂存行按 subject_id, data_time, series_tag 排序后以 CSV 写到 w（格式见 csvio.go，表头为
// data_time,subject_id,series_tag,<值列…>）。流式写出，不在内存里攒整个分区；压缩、落盘与 sha256 由调用方负责。
func (s *Store) ExportCSV(ctx context.Context, task Task, p PartitionState, out io.Writer) error {
	if err := task.Validate(); err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 256<<10)
	header := []csvField{{value: "data_time"}, {value: "subject_id"}, {value: "series_tag"}}
	cols := make([]string, len(task.Columns))
	for i, c := range task.Columns {
		header = append(header, csvField{value: c.Name})
		cols[i] = "t." + quote(c.Name)
	}
	if err := writeCSVRecord(w, header); err != nil {
		return err
	}
	selectCols := ""
	if len(cols) > 0 {
		selectCols = ", " + strings.Join(cols, ", ")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT t.data_time, t.subject_id, t.series_tag%s FROM %s t WHERE t.data_time >= ? AND t.data_time < ? ORDER BY t.subject_id, t.data_time, t.series_tag`,
		selectCols, quote(task.Table())), nanos(p.Start), nanos(p.End))
	if err != nil {
		return err
	}
	defer rows.Close()
	var at int64
	var subject, tag string
	values := make([]any, len(task.Columns))
	targets := append([]any{&at, &subject, &tag}, make([]any, len(task.Columns))...)
	for i := range values {
		targets[3+i] = &values[i]
	}
	record := make([]csvField, 3+len(task.Columns))
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		record[0], record[1], record[2] = csvField{value: formatTime(fromNanos(at))}, csvField{value: subject}, csvField{value: tag}
		for i, c := range task.Columns {
			if record[3+i], err = formatValue(c.Type, values[i]); err != nil {
				return err
			}
		}
		if err := writeCSVRecord(w, record); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return w.Flush()
}

// MarkExported 记录分区已导出：导出时间、文件相对路径与 sha256。新版本文件应当立刻尝试上传，不继承旧版本的退避：
// 上传失败次数、重试时刻与错误清零（远端字段保留，sha256 变了就自动成为“待上传”）。
func (s *Store) MarkExported(ctx context.Context, spaceID, datasetID, key string, exportedAt time.Time, relPath, sha string) error {
	result, err := s.db.ExecContext(ctx, markExported,
		nanos(exportedAt), relPath, sha, spaceID, datasetID, key)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("分区 %s 没有登记或已定稿", key)
	}
	return nil
}

// Finalize 把分区定稿：每个分区一个事务，删除它的暂存行、状态置为 final，并把统计记进分区状态。
// 水位由调用方随后用 AdvanceWatermark 单独推进：只定稿成功时最多多一次读回，只推进水位成功时留到下次再定稿，都不丢数据。
func (s *Store) Finalize(ctx context.Context, task Task, keys []string, finalizedAt time.Time) error {
	for _, key := range keys {
		if err := s.tx(ctx, func(tx *sql.Tx) error { return finalizeOne(ctx, tx, task, key, finalizedAt) }); err != nil {
			return err
		}
	}
	return nil
}

func finalizeOne(ctx context.Context, tx *sql.Tx, task Task, key string, finalizedAt time.Time) error {
	var start, end, changed int64
	var status string
	var exported sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT range_start, range_end, status, last_changed_at, exported_at FROM partition_state WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`,
		task.SpaceID, task.DatasetID, key).Scan(&start, &end, &status, &changed, &exported)
	if err != nil {
		return fmt.Errorf("定稿分区 %s: %w", key, err)
	}
	if status == StatusFinal {
		return nil
	}
	if !exported.Valid || changed > exported.Int64 {
		return fmt.Errorf("分区 %s 有尚未导出的变化，不能定稿", key)
	}
	// 删除暂存行之前把统计记进分区状态：定稿后登记（行数、标的数、数据时间范围）只能靠它。
	var rows, subjects int64
	var minTime, maxTime sql.NullInt64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*), count(DISTINCT subject_id), min(data_time), max(data_time) FROM %s WHERE data_time >= ? AND data_time < ?`, quote(task.Table())), start, end).
		Scan(&rows, &subjects, &minTime, &maxTime); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE data_time >= ? AND data_time < ?`, quote(task.Table())), start, end); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE partition_state SET status = 'final', row_count = ?, subject_count = ?, min_data_time = ?, max_data_time = ?, finalized_at = ?, report_pending = 1
WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`, rows, subjects, minTime, maxTime, nanos(finalizedAt), task.SpaceID, task.DatasetID, key)
	return err
}

// RestorePartitions 按 mgr 下发的分区登记补齐本地缺的分区状态（暂存库丢失后重建用）：只插入本地没有的分区键，report_pending 为 0。
// 远端副本字段一并恢复：远端是当前版本的就是“已上传”，不是的就是“待上传”。
// 按“有没有文件”判断，不看登记状态：有文件路径的一律按 final 恢复（统计、路径、sha256、导出次数取登记值；登记不是 final
// 或没有定稿时间时定稿时间取 now）。它的暂存行已随暂存库丢失，按未定稿恢复会在定稿时从空暂存表算出 0 行；按 final 恢复后，
// 再读到它的行会先读回原文件合并。没有文件的不恢复（行只在丢掉的暂存库里，重读即可）。返回插入的分区数。
func (s *Store) RestorePartitions(ctx context.Context, spaceID, datasetID string, regs []PartitionState, now time.Time) (int, error) {
	inserted := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, p := range regs {
			if p.FilePath == "" {
				continue
			}
			if p.SHA256 == "" || p.ExportedAt.IsZero() {
				return fmt.Errorf("分区 %s 的登记缺少 sha256 或导出时间，无法恢复", p.Key)
			}
			finalized := p.FinalizedAt
			if p.Status != StatusFinal || finalized.IsZero() {
				finalized = now
			}
			changed := p.LastChangedAt
			if changed.IsZero() || changed.After(p.ExportedAt) {
				changed = p.ExportedAt
			}
			if p.Remote.Key != "" && p.Remote.SHA256 == "" {
				return fmt.Errorf("分区 %s 的登记有远端对象键却没有 sha256，无法恢复", p.Key)
			}
			result, err := tx.ExecContext(ctx, restorePartition,
				spaceID, datasetID, p.Key, nanos(p.Start), nanos(p.End), nanos(changed), nanos(p.ExportedAt),
				p.FilePath, p.SHA256, p.ExportCount, int64(p.RowCount), int64(p.SubjectCount), nullNanos(p.MinDataTime), nullNanos(p.MaxDataTime), nanos(finalized),
				p.Remote.Key, p.Remote.SHA256, p.Remote.ETag, nullNanos(p.Remote.UploadedAt), nullNanos(p.Remote.VerifiedAt), p.Remote.Error)
			if err != nil {
				return fmt.Errorf("恢复分区 %s: %w", p.Key, err)
			}
			if n, _ := result.RowsAffected(); n > 0 {
				inserted++
			}
		}
		return nil
	})
	return inserted, err
}

// openCSV 打开归档文件；gzip 压缩的（按魔数判断）先解压。
func openCSV(path string) (*csvReader, io.Closer, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReaderSize(file, 256<<10)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			_ = file.Close()
			return nil, nil, err
		}
		return &csvReader{r: bufio.NewReaderSize(zr, 256<<10)}, closers{zr, file}, nil
	}
	return &csvReader{r: br}, file, nil
}

type closers []io.Closer

func (c closers) Close() error {
	var first error
	for _, closer := range c {
		if err := closer.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// reopenBatchRows 是读回时每条 INSERT 的行数上限。
const reopenBatchRows = 500

// ReopenFinal 把已定稿分区的原文件（path 是绝对路径，调用方先校验 sha256）按列名读回暂存表，分区标回 exported：
// 文件里没有的新列为 NULL，同键以文件里的值为准。读回的行与导出时一致，所以 last_changed_at 置为 exported_at，
// 不会因为重新打开就触发重写。
func (s *Store) ReopenFinal(ctx context.Context, task Task, key, path string) error {
	if err := task.Validate(); err != nil {
		return err
	}
	reader, closer, err := openCSV(path)
	if err != nil {
		return fmt.Errorf("读取归档文件 %s 的表头: %w", path, err)
	}
	defer closer.Close()
	headerFields, err := reader.read()
	if err != nil {
		return fmt.Errorf("读取归档文件 %s 的表头: %w", path, err)
	}
	columnType := map[string]string{"data_time": TypeTime, "subject_id": TypeString, "series_tag": TypeString}
	for _, c := range task.Columns {
		columnType[c.Name] = c.Type
	}
	var names, types []string
	for _, f := range headerFields {
		typ, ok := columnType[f.value]
		if !ok {
			return fmt.Errorf("归档文件 %s 有列 %q，数据集已经没有这一列", path, f.value)
		}
		names, types = append(names, quote(f.value)), append(types, typ)
	}
	insert := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s) VALUES `, quote(task.Table()), strings.Join(names, ", "))
	placeholder := "(" + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + ")"
	return s.tx(ctx, func(tx *sql.Tx) error {
		var placeholders []string
		var args []any
		flush := func() error {
			if len(placeholders) == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, insert+strings.Join(placeholders, ","), args...)
			placeholders, args = placeholders[:0], args[:0]
			return err
		}
		for {
			record, err := reader.read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("读回归档文件 %s 第 %d 行：%w", path, reader.line, err)
			}
			if len(record) != len(names) {
				return fmt.Errorf("读回归档文件 %s 第 %d 行：有 %d 个字段，表头是 %d 列", path, reader.line, len(record), len(names))
			}
			for i, f := range record {
				value, err := parseValue(types[i], f)
				if err != nil {
					return fmt.Errorf("读回归档文件 %s 第 %d 行 %s 列：%w", path, reader.line, names[i], err)
				}
				args = append(args, value)
			}
			placeholders = append(placeholders, placeholder)
			if len(placeholders) >= reopenBatchRows {
				if err := flush(); err != nil {
					return fmt.Errorf("读回归档文件 %s: %w", path, err)
				}
			}
		}
		if err := flush(); err != nil {
			return fmt.Errorf("读回归档文件 %s: %w", path, err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE partition_state SET status = 'exported', report_pending = 1, finalized_at = NULL, last_changed_at = COALESCE(exported_at, last_changed_at) WHERE space_id = ? AND dataset_id = ? AND partition_key = ? AND status = 'final'`,
			task.SpaceID, task.DatasetID, key)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return fmt.Errorf("分区 %s 不是已定稿分区", key)
		}
		return nil
	})
}

// PendingReports 返回最新状态还没有被 mgr 确认登记的分区（含已定稿的），按分区键升序。
func (s *Store) PendingReports(ctx context.Context, spaceID, datasetID string) ([]PartitionState, error) {
	rows, err := s.db.QueryContext(ctx, queryPartitions3, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PartitionState
	for rows.Next() {
		p, err := scanPartition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkReported 在 mgr 确认登记之后清除分区的待上报标记。
func (s *Store) MarkReported(ctx context.Context, spaceID, datasetID string, keys []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, key := range keys {
			if _, err := tx.ExecContext(ctx, `UPDATE partition_state SET report_pending = 0 WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`, spaceID, datasetID, key); err != nil {
				return err
			}
		}
		return nil
	})
}

// 以下查询语句整条写成常量，避免在调用处拼接 SQL。
const (
	queryPartitions1 = `SELECT ` + partitionColumns + ` FROM partition_state WHERE space_id = ? AND dataset_id = ? ORDER BY partition_key`
	queryPartitions2 = `SELECT ` + partitionColumns + ` FROM partition_state WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`
	queryPartitions3 = `SELECT ` + partitionColumns + ` FROM partition_state WHERE space_id = ? AND dataset_id = ? AND report_pending = 1 ORDER BY partition_key`
	markExported     = `UPDATE partition_state SET status = 'exported', exported_at = ?, file_path = ?, file_sha256 = ?, export_count = export_count + 1, report_pending = 1,
    remote_attempts = 0, remote_retry_at = NULL, remote_error = ''
WHERE space_id = ? AND dataset_id = ? AND partition_key = ? AND status != 'final'`
	restorePartition = `INSERT INTO partition_state (space_id, dataset_id, partition_key, range_start, range_end, status, last_changed_at, exported_at,
    file_path, file_sha256, export_count, row_count, subject_count, min_data_time, max_data_time, finalized_at, report_pending,
    remote_key, remote_sha256, remote_etag, remote_uploaded_at, remote_verified_at, remote_error)
VALUES (?, ?, ?, ?, ?, 'final', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)
ON CONFLICT (space_id, dataset_id, partition_key) DO NOTHING`
)
