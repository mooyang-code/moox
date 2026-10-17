package staging

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Row 是一条读到的时序行；Values 与 Task.Columns 一一对应，nil 表示缺失值。
type Row struct {
	SubjectID string
	DataTime  time.Time
	SeriesTag string
	Values    []any
}

// PartitionRef 是行所属的分区：键与 [Start, End) 范围（UTC）。
type PartitionRef struct {
	Key   string
	Start time.Time
	End   time.Time
}

// StageResult 是一批行写入暂存区的结果。
type StageResult struct {
	Added   uint64
	Changed uint64
	// Touched 是有新增或变化行的分区。
	Touched map[string]PartitionRef
}

// maxParams 限制一条 INSERT 的参数个数。
const maxParams = 6000

// Stage 把一批行写入暂存区：先进临时表，与暂存表按主键比对，只把新增和值有变化的行写进暂存表，
// 并在同一个事务里更新受影响分区的 last_changed_at。值没变的行不产生写入。
func (s *Store) Stage(ctx context.Context, task Task, rows []Row, partition func(time.Time) PartitionRef, now time.Time) (StageResult, error) {
	result := StageResult{Touched: map[string]PartitionRef{}}
	if len(rows) == 0 {
		return result, nil
	}
	if err := task.Validate(); err != nil {
		return result, err
	}
	// 同一批里主键重复的行以最后一条为准。
	type key struct {
		subject string
		at      int64
		tag     string
	}
	index := make(map[key]int, len(rows))
	unique := make([]Row, 0, len(rows))
	for _, row := range rows {
		if len(row.Values) != len(task.Columns) {
			return result, fmt.Errorf("row %s@%s has %d values, want %d", row.SubjectID, row.DataTime.Format(time.RFC3339), len(row.Values), len(task.Columns))
		}
		k := key{row.SubjectID, row.DataTime.UnixMicro(), row.SeriesTag}
		if at, ok := index[k]; ok {
			unique[at] = row
			continue
		}
		index[k] = len(unique)
		unique = append(unique, row)
	}
	names := make([]string, len(task.Columns))
	typedefs := make([]string, len(task.Columns))
	for i, c := range task.Columns {
		names[i] = quote(c.Name)
		typ, _ := sqlType(c.Type)
		typedefs[i] = quote(c.Name) + " " + typ
	}
	valueList := ""
	if len(names) > 0 {
		valueList = ", " + strings.Join(names, ", ")
	}
	table := quote(task.Table())
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS incoming; DROP TABLE IF EXISTS changed`); err != nil {
			return err
		}
		extra := ""
		if len(typedefs) > 0 {
			extra = ", " + strings.Join(typedefs, ", ")
		}
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE incoming (subject_id VARCHAR, data_time TIMESTAMP, series_tag VARCHAR`+extra+`, pkey VARCHAR, pstart TIMESTAMP, pend TIMESTAMP)`); err != nil {
			return err
		}
		perRow := 6 + len(names)
		chunk := maxParams / perRow
		if chunk < 1 {
			chunk = 1
		}
		for start := 0; start < len(unique); start += chunk {
			end := min(start+chunk, len(unique))
			var placeholders []string
			var args []any
			for _, row := range unique[start:end] {
				ref := partition(row.DataTime)
				placeholders = append(placeholders, "("+strings.TrimSuffix(strings.Repeat("?,", perRow), ",")+")")
				args = append(args, row.SubjectID, row.DataTime.UTC(), row.SeriesTag)
				args = append(args, row.Values...)
				args = append(args, ref.Key, ref.Start.UTC(), ref.End.UTC())
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO incoming VALUES `+strings.Join(placeholders, ","), args...); err != nil {
				return fmt.Errorf("stage incoming rows: %w", err)
			}
		}
		var differs []string
		for _, c := range task.Columns {
			differs = append(differs, fmt.Sprintf("i.%s IS DISTINCT FROM s.%s", quote(c.Name), quote(c.Name)))
		}
		where := "s.subject_id IS NULL"
		if len(differs) > 0 {
			where += " OR " + strings.Join(differs, " OR ")
		}
		var selectCols []string
		for _, n := range names {
			selectCols = append(selectCols, "i."+n)
		}
		sel := ""
		if len(selectCols) > 0 {
			sel = ", " + strings.Join(selectCols, ", ")
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE TEMP TABLE changed AS SELECT i.subject_id, i.data_time, i.series_tag%s, i.pkey, i.pstart, i.pend, (s.subject_id IS NOT NULL) AS existed
FROM incoming i LEFT JOIN %s s ON i.subject_id = s.subject_id AND i.data_time = s.data_time AND i.series_tag = s.series_tag WHERE %s`, sel, table, where)); err != nil {
			return fmt.Errorf("compare staged rows: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT OR REPLACE INTO %s (subject_id, data_time, series_tag%s) SELECT subject_id, data_time, series_tag%s FROM changed`, table, valueList, valueList)); err != nil {
			return fmt.Errorf("write changed rows: %w", err)
		}
		stats, err := tx.QueryContext(ctx, `SELECT pkey, pstart, pend, count(*) FILTER (WHERE existed), count(*) FILTER (WHERE NOT existed) FROM changed GROUP BY pkey, pstart, pend`)
		if err != nil {
			return err
		}
		type touched struct {
			ref     PartitionRef
			changed uint64
			added   uint64
		}
		var list []touched
		for stats.Next() {
			var t touched
			var changed, added int64
			if err := stats.Scan(&t.ref.Key, &t.ref.Start, &t.ref.End, &changed, &added); err != nil {
				_ = stats.Close()
				return err
			}
			t.ref.Start, t.ref.End = t.ref.Start.UTC(), t.ref.End.UTC()
			t.changed, t.added = uint64(changed), uint64(added)
			list = append(list, t)
		}
		if err := stats.Close(); err != nil {
			return err
		}
		for _, t := range list {
			result.Changed += t.changed
			result.Added += t.added
			result.Touched[t.ref.Key] = t.ref
			if _, err := tx.ExecContext(ctx, `INSERT INTO partition_state (space_id, dataset_id, partition_key, range_start, range_end, status, last_changed_at)
VALUES (?, ?, ?, ?, ?, 'open', ?)
ON CONFLICT (space_id, dataset_id, partition_key) DO UPDATE SET last_changed_at = excluded.last_changed_at,
    status = CASE WHEN partition_state.status = 'final' THEN 'open' ELSE partition_state.status END`,
				task.SpaceID, task.DatasetID, t.ref.Key, t.ref.Start, t.ref.End, now.UTC()); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `DROP TABLE IF EXISTS incoming; DROP TABLE IF EXISTS changed`)
		return err
	})
	if err != nil {
		return StageResult{Touched: map[string]PartitionRef{}}, err
	}
	return result, nil
}
