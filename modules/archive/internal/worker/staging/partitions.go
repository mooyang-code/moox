package staging

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
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
	SHA256        string
	ExportCount   uint32
}

// NeedsExport 报告分区是否需要导出：从未导出，或导出之后又有变化。
func (p PartitionState) NeedsExport() bool {
	if p.Status == StatusFinal {
		return false
	}
	return p.ExportedAt.IsZero() || p.LastChangedAt.After(p.ExportedAt)
}

const partitionColumns = `partition_key, range_start, range_end, status, last_changed_at, exported_at, file_sha256, export_count`

func scanPartition(row interface{ Scan(...any) error }) (PartitionState, error) {
	var p PartitionState
	var exported sql.NullTime
	if err := row.Scan(&p.Key, &p.Start, &p.End, &p.Status, &p.LastChangedAt, &exported, &p.SHA256, &p.ExportCount); err != nil {
		return PartitionState{}, err
	}
	p.Start, p.End, p.LastChangedAt = p.Start.UTC(), p.End.UTC(), p.LastChangedAt.UTC()
	if exported.Valid {
		p.ExportedAt = exported.Time.UTC()
	}
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
		spaceID, datasetID, p.Key, p.Start.UTC(), p.End.UTC(), p.Status, p.LastChangedAt.UTC(), nullTime(p.ExportedAt), p.SHA256)
	return err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
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
	var min, max sql.NullTime
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*), count(DISTINCT subject_id), min(data_time), max(data_time) FROM %s WHERE data_time >= ? AND data_time < ?`, quote(task.Table())),
		p.Start.UTC(), p.End.UTC()).Scan(&rows, &subjects, &min, &max)
	if err != nil {
		return out, err
	}
	out.Rows, out.Subjects = uint64(rows), uint64(subjects)
	if min.Valid {
		out.Min, out.Max = min.Time.UTC(), max.Time.UTC()
	}
	return out, nil
}

// Export 把分区的暂存行按 subject_id, data_time, series_tag 排序后导出成 gzip CSV 到 dest（调用方负责 .partial/rename）。
// 表头为 data_time,subject_id,series_tag,<值列…>；data_time 为 UTC RFC3339。
func (s *Store) Export(ctx context.Context, task Task, p PartitionState, dest string) error {
	if err := task.Validate(); err != nil {
		return err
	}
	var cols []string
	for _, c := range task.Columns {
		cols = append(cols, quote(c.Name))
	}
	query := fmt.Sprintf(`COPY (SELECT strftime(t.data_time, '%%Y-%%m-%%dT%%H:%%M:%%SZ') AS data_time, t.subject_id, t.series_tag%s FROM %s t WHERE t.data_time >= %s AND t.data_time < %s ORDER BY t.subject_id, t.data_time, t.series_tag) TO %s (FORMAT csv, HEADER, COMPRESSION gzip)`,
		prefixed(cols), quote(task.Table()), timestampLiteral(p.Start), timestampLiteral(p.End), literal(dest))
	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("export partition %s: %w", p.Key, err)
	}
	return nil
}

func prefixed(cols []string) string {
	if len(cols) == 0 {
		return ""
	}
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = "t." + c
	}
	return ", " + strings.Join(out, ", ")
}

func timestampLiteral(t time.Time) string {
	return "TIMESTAMP " + literal(t.UTC().Format("2006-01-02 15:04:05.000000"))
}

// MarkExported 记录分区已导出。
func (s *Store) MarkExported(ctx context.Context, spaceID, datasetID, key string, exportedAt time.Time, sha string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE partition_state SET status = 'exported', exported_at = ?, file_sha256 = ?, export_count = export_count + 1 WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`,
		exportedAt.UTC(), sha, spaceID, datasetID, key)
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
func (s *Store) Finalize(ctx context.Context, task Task, keys []string, watermark time.Time, setWatermark bool) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, key := range keys {
			var start, end time.Time
			var status string
			var exported sql.NullTime
			var changed time.Time
			err := tx.QueryRowContext(ctx, `SELECT range_start, range_end, status, last_changed_at, exported_at FROM partition_state WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`,
				task.SpaceID, task.DatasetID, key).Scan(&start, &end, &status, &changed, &exported)
			if err != nil {
				return fmt.Errorf("finalize partition %s: %w", key, err)
			}
			if !exported.Valid || changed.After(exported.Time) {
				return fmt.Errorf("partition %s has changes that were not exported; refusing to finalize", key)
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE data_time >= ? AND data_time < ?`, quote(task.Table())), start, end); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE partition_state SET status = 'final' WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`, task.SpaceID, task.DatasetID, key); err != nil {
				return err
			}
		}
		if setWatermark {
			_, err := tx.ExecContext(ctx, `INSERT INTO task_state (space_id, dataset_id, scan_watermark) VALUES (?, ?, ?)
ON CONFLICT (space_id, dataset_id) DO UPDATE SET scan_watermark = excluded.scan_watermark`, task.SpaceID, task.DatasetID, watermark.UTC())
			return err
		}
		return nil
	})
}

// CSVHeader 读取 gzip CSV 的表头。
func CSVHeader(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	zr, err := gzip.NewReader(bufio.NewReader(file))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return csv.NewReader(zr).Read()
}

// ReopenFinal 把已定稿分区的原文件按列名读回暂存表并把分区标回 open：文件里没有的新列为 NULL，同键以文件里的值为准。
// 读回的行与导出时一致，所以 last_changed_at 置为 exported_at，不会因为重新打开就触发重写。
func (s *Store) ReopenFinal(ctx context.Context, task Task, key, path string) error {
	if err := task.Validate(); err != nil {
		return err
	}
	header, err := CSVHeader(path)
	if err != nil {
		return fmt.Errorf("read archive file header %s: %w", path, err)
	}
	known := map[string]string{"data_time": "TIMESTAMP", "subject_id": "VARCHAR", "series_tag": "VARCHAR"}
	columnType := map[string]string{}
	for _, c := range task.Columns {
		typ, _ := sqlType(c.Type)
		known[c.Name], columnType[c.Name] = typ, typ
	}
	var types, selectNames []string
	for _, name := range header {
		typ, ok := known[name]
		if !ok {
			return fmt.Errorf("archive file %s has column %q which the dataset no longer has", path, name)
		}
		types = append(types, literal(name)+": "+literal(typ))
		selectNames = append(selectNames, quote(name))
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s) SELECT %s FROM read_csv(%s, header = true, union_by_name = true, allow_quoted_nulls = false, types = {%s})`,
			quote(task.Table()), strings.Join(selectNames, ", "), strings.Join(selectNames, ", "), literal(path), strings.Join(types, ", "))
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("read back archive file %s: %w", path, err)
		}
		result, err := tx.ExecContext(ctx, `UPDATE partition_state SET status = 'open', last_changed_at = COALESCE(exported_at, last_changed_at) WHERE space_id = ? AND dataset_id = ? AND partition_key = ? AND status = 'final'`,
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
