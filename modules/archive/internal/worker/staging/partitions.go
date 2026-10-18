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
	"strconv"
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
	SHA256        string
	ExportCount   uint32
	// 定稿时记下的统计（定稿后暂存行已删除，登记要靠它）；未定稿的分区用 Stats 现算。
	RowCount     uint64
	SubjectCount uint64
	MinDataTime  time.Time
	MaxDataTime  time.Time
	FinalizedAt  time.Time
	// ReportPending 为真表示分区的最新状态还没有被 mgr 确认登记。
	ReportPending bool
}

// NeedsExport 报告分区是否需要导出：从未导出，或导出之后又有变化。
func (p PartitionState) NeedsExport() bool {
	if p.Status == StatusFinal {
		return false
	}
	return p.ExportedAt.IsZero() || p.LastChangedAt.After(p.ExportedAt)
}

const partitionColumns = `partition_key, range_start, range_end, status, last_changed_at, exported_at, file_sha256, export_count, row_count, subject_count, min_data_time, max_data_time, finalized_at, report_pending`

func scanPartition(row interface{ Scan(...any) error }) (PartitionState, error) {
	var p PartitionState
	var start, end, changed, rows, subjects, pending int64
	var exported, minTime, maxTime, finalized sql.NullInt64
	if err := row.Scan(&p.Key, &start, &end, &p.Status, &changed, &exported, &p.SHA256, &p.ExportCount, &rows, &subjects, &minTime, &maxTime, &finalized, &pending); err != nil {
		return PartitionState{}, err
	}
	p.Start, p.End, p.LastChangedAt = fromNanos(start), fromNanos(end), fromNanos(changed)
	p.ExportedAt, p.MinDataTime, p.MaxDataTime, p.FinalizedAt = scanNanos(exported), scanNanos(minTime), scanNanos(maxTime), scanNanos(finalized)
	p.RowCount, p.SubjectCount, p.ReportPending = uint64(rows), uint64(subjects), pending != 0
	return p, nil
}

// Partitions 按分区键升序返回任务的全部分区状态。
func (s *Store) Partitions(ctx context.Context, spaceID, datasetID string) ([]PartitionState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+partitionColumns+` FROM partition_state WHERE space_id = ? AND dataset_id = ? ORDER BY partition_key`, spaceID, datasetID)
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
	p, err := scanPartition(s.db.QueryRowContext(ctx, `SELECT `+partitionColumns+` FROM partition_state WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`, spaceID, datasetID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return PartitionState{}, false, nil
	}
	return p, err == nil, err
}

// RegisterPartition 登记一个还没有数据的分区（补拉区间读到定稿分区之前不需要；主要给测试和重建登记使用）。
func (s *Store) RegisterPartition(ctx context.Context, spaceID, datasetID string, p PartitionState) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO partition_state (space_id, dataset_id, partition_key, range_start, range_end, status, last_changed_at, exported_at, file_sha256)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (space_id, dataset_id, partition_key) DO NOTHING`,
		spaceID, datasetID, p.Key, nanos(p.Start), nanos(p.End), p.Status, nanos(p.LastChangedAt), nullNanos(p.ExportedAt), p.SHA256)
	return err
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

// Export 把分区的暂存行按 subject_id, data_time, series_tag 排序后导出成 gzip CSV 到 dest（调用方负责 .partial/rename）。
// 格式见 csvio.go：表头为 data_time,subject_id,series_tag,<值列…>。
func (s *Store) Export(ctx context.Context, task Task, p PartitionState, dest string) error {
	return s.ExportCSV(ctx, task, p, dest, true)
}

// ExportCSV 同 Export，compress 为 false 时写未压缩的 CSV。流式写出，不在内存里攒整个分区。
func (s *Store) ExportCSV(ctx context.Context, task Task, p PartitionState, dest string, compress bool) (err error) {
	if err := task.Validate(); err != nil {
		return err
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			err = fmt.Errorf("export partition %s: %w", p.Key, err)
		}
	}()
	var out io.Writer = file
	var zw *gzip.Writer
	if compress {
		zw = gzip.NewWriter(file)
		out = zw
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
	if err := w.Flush(); err != nil {
		return err
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			return err
		}
	}
	return file.Sync()
}

// MarkExported 记录分区已导出。
func (s *Store) MarkExported(ctx context.Context, spaceID, datasetID, key string, exportedAt time.Time, sha string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE partition_state SET status = 'exported', exported_at = ?, file_sha256 = ?, export_count = export_count + 1, report_pending = 1 WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`,
		nanos(exportedAt), sha, spaceID, datasetID, key)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("partition %s is not registered", key)
	}
	return nil
}

// Finalize 在一个事务里把分区定稿（删除它们的暂存行、状态置为 final），并在 setWatermark 为真时把任务的读取水位推进到 watermark。
// 定稿与推进水位必须一起提交：事务失败时两者都不变。
func (s *Store) Finalize(ctx context.Context, task Task, keys []string, watermark, finalizedAt time.Time, setWatermark bool) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, key := range keys {
			var start, end, changed int64
			var status string
			var exported sql.NullInt64
			err := tx.QueryRowContext(ctx, `SELECT range_start, range_end, status, last_changed_at, exported_at FROM partition_state WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`,
				task.SpaceID, task.DatasetID, key).Scan(&start, &end, &status, &changed, &exported)
			if err != nil {
				return fmt.Errorf("finalize partition %s: %w", key, err)
			}
			if !exported.Valid || changed > exported.Int64 {
				return fmt.Errorf("partition %s has changes that were not exported; refusing to finalize", key)
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
			if _, err := tx.ExecContext(ctx, `UPDATE partition_state SET status = 'final', row_count = ?, subject_count = ?, min_data_time = ?, max_data_time = ?, finalized_at = ?, report_pending = 1
WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`, rows, subjects, minTime, maxTime, nanos(finalizedAt), task.SpaceID, task.DatasetID, key); err != nil {
				return err
			}
		}
		if setWatermark {
			// 水位一旦前进，首次运行的起点就不再需要。
			_, err := tx.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, scan_watermark, first_start) VALUES (?, ?, ?, NULL)
ON CONFLICT (space_id, dataset_id) DO UPDATE SET scan_watermark = excluded.scan_watermark, first_start = NULL`, task.SpaceID, task.DatasetID, nanos(watermark))
			return err
		}
		return nil
	})
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

// CSVHeader 读取 CSV 的表头。
func CSVHeader(path string) ([]string, error) {
	reader, closer, err := openCSV(path)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	fields, err := reader.read()
	if err != nil {
		return nil, err
	}
	header := make([]string, len(fields))
	for i, f := range fields {
		header[i] = f.value
	}
	return header, nil
}

// reopenBatchRows 是读回时每条 INSERT 的行数上限。
const reopenBatchRows = 500

// ReopenFinal 把已定稿分区的原文件按列名读回暂存表并把分区标回 open：文件里没有的新列为 NULL，同键以文件里的值为准。
// 读回的行与导出时一致，所以 last_changed_at 置为 exported_at，不会因为重新打开就触发重写。
func (s *Store) ReopenFinal(ctx context.Context, task Task, key, path string) error {
	if err := task.Validate(); err != nil {
		return err
	}
	reader, closer, err := openCSV(path)
	if err != nil {
		return fmt.Errorf("read archive file header %s: %w", path, err)
	}
	defer closer.Close()
	headerFields, err := reader.read()
	if err != nil {
		return fmt.Errorf("read archive file header %s: %w", path, err)
	}
	columnType := map[string]string{"data_time": TypeTime, "subject_id": TypeString, "series_tag": TypeString}
	for _, c := range task.Columns {
		columnType[c.Name] = c.Type
	}
	var names, types []string
	for _, f := range headerFields {
		typ, ok := columnType[f.value]
		if !ok {
			return fmt.Errorf("archive file %s has column %q which the dataset no longer has", path, f.value)
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
				return fmt.Errorf("read back archive file %s: %w", path, err)
			}
			if len(record) != len(names) {
				return fmt.Errorf("read back archive file %s: line %d has %d fields, want %d", path, reader.line, len(record), len(names))
			}
			for i, f := range record {
				value, err := parseValue(types[i], f)
				if err != nil {
					return fmt.Errorf("read back archive file %s: line %d column %s: %w", path, reader.line, names[i], err)
				}
				args = append(args, value)
			}
			placeholders = append(placeholders, placeholder)
			if len(placeholders) >= reopenBatchRows {
				if err := flush(); err != nil {
					return fmt.Errorf("read back archive file %s: %w", path, err)
				}
			}
		}
		if err := flush(); err != nil {
			return fmt.Errorf("read back archive file %s: %w", path, err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE partition_state SET status = 'open', report_pending = 1, finalized_at = NULL, last_changed_at = COALESCE(exported_at, last_changed_at) WHERE space_id = ? AND dataset_id = ? AND partition_key = ? AND status = 'final'`,
			task.SpaceID, task.DatasetID, key)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return fmt.Errorf("partition %s is not a final partition", key)
		}
		return nil
	})
}

// PendingReports 返回最新状态还没有被 mgr 确认登记的分区（含已定稿的），按分区键升序。
func (s *Store) PendingReports(ctx context.Context, spaceID, datasetID string) ([]PartitionState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+partitionColumns+` FROM partition_state WHERE space_id = ? AND dataset_id = ? AND report_pending = 1 ORDER BY partition_key`, spaceID, datasetID)
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

// FirstStart 返回首次运行（还没有水位）时记下的读取起点。
func (s *Store) FirstStart(ctx context.Context, spaceID, datasetID string) (time.Time, bool, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT first_start FROM task_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID).Scan(&v)
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

// SetFirstStart 记下首次运行的读取起点；已有更早的起点时保留更早的。首次运行失败后重试必须从同一个起点读起，
// 否则会跳过两次起点之间尚未读到的数据。
func (s *Store) SetFirstStart(ctx context.Context, spaceID, datasetID string, start time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, first_start) VALUES (?, ?, ?)
ON CONFLICT (space_id, dataset_id) DO UPDATE SET first_start = CASE WHEN task_state.first_start IS NULL OR excluded.first_start < task_state.first_start THEN excluded.first_start ELSE task_state.first_start END`,
		spaceID, datasetID, nanos(start))
	return err
}

// BindEpoch 把暂存状态绑定到任务实例。已绑定的实例与当前不同，说明任务被删除后重建过：旧任务遗留的水位、
// 首次起点和暂存数据一律丢弃，新任务从自己的回补设置开始。没有绑定记录时直接采用当前实例。
// 返回 true 表示丢弃了旧状态。
func (s *Store) BindEpoch(ctx context.Context, spaceID, datasetID, epoch string) (bool, error) {
	if epoch == "" {
		return false, nil
	}
	var stored sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT task_epoch FROM task_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	dropped := false
	if stored.Valid && stored.String != "" && stored.String != epoch {
		if err := s.DropTask(ctx, spaceID, datasetID); err != nil {
			return false, err
		}
		dropped = true
	}
	if !stored.Valid || stored.String != epoch || dropped {
		_, err = s.db.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, task_epoch) VALUES (?, ?, ?)
ON CONFLICT (space_id, dataset_id) DO UPDATE SET task_epoch = excluded.task_epoch`, spaceID, datasetID, epoch)
		if err != nil {
			return false, err
		}
	}
	return dropped, nil
}

// DropIfUnbound 丢弃还没绑定任务实例的旧状态（升级前留下的）。已绑定实例的状态一律保留：
// 迟到的删除通知不能清掉新任务的数据，新旧任务的区分交给 BindEpoch。
func (s *Store) DropIfUnbound(ctx context.Context, spaceID, datasetID string) error {
	var stored sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT task_epoch FROM task_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && stored.Valid && stored.String != "" {
		return nil
	}
	// 没有 task_state 记录也算未绑定：可能留有只补拉过或首次回补中途失败的暂存表和分区状态。
	return s.DropTask(ctx, spaceID, datasetID)
}

// DropDeleted 按删除通知清理任务状态：通知带着被删任务的实例标识，只清理属于该实例（或尚未绑定实例）的状态；
// 状态已经属于另一个实例（任务已重建并绑定）时，迟到的通知不能删掉它。通知没带实例标识时按旧行为清理。
func (s *Store) DropDeleted(ctx context.Context, spaceID, datasetID, epoch string) error {
	if epoch != "" {
		var stored sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT task_epoch FROM task_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID).Scan(&stored)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && stored.Valid && stored.String != "" && stored.String != epoch && !epochNewer(epoch, stored.String) {
			return nil
		}
	}
	return s.DropTask(ctx, spaceID, datasetID)
}

// epochNewer 报告通知里的实例标识 a 是否比已绑定的 b 更新（实例标识是单调递增的任务 ID）；
// 删除后又被重建、再删除的任务，最终的通知只剩较新的标识，旧实例遗留的状态仍要清理。
func epochNewer(a, b string) bool {
	x, errA := strconv.ParseInt(a, 10, 64)
	y, errB := strconv.ParseInt(b, 10, 64)
	return errA == nil && errB == nil && x > y
}
