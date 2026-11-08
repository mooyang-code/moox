package staging

import (
	"context"
	"database/sql"
	"errors"
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

// StageResult 是一批行写入暂存区的结果（同一批里主键重复的行先按最后一条去重）。
type StageResult struct {
	// Added 是暂存表里原来没有的行（已写入）。
	Added uint64
	// Changed 是同键已存在但值不同的行：StageReplace 下已覆盖，StageFillGaps 下未写入。
	Changed uint64
	// Same 是同键已存在且值相同的行（不产生写入）。
	Same uint64
}

// StageMode 决定同键已存在且值不同的行怎么处理。
type StageMode int

const (
	// StageReplace 用新值覆盖（读取 Storage 与覆盖导入）。
	StageReplace StageMode = iota
	// StageFillGaps 只写原来没有的行，已存在的以暂存表为准（不带 --overwrite 的导入）。
	StageFillGaps
)

// maxParams 限制一条 INSERT 的参数个数（SQLite 默认上限 32766）。
const maxParams = 6000

// Stage 把一批行写入暂存区：先进临时表，与暂存表按主键比对，只把新增和（StageReplace 下）值有变化的行写进暂存表，
// 并在同一个事务里更新实际有写入的分区的 last_changed_at。值没变的行、StageFillGaps 下值不同的行都不产生写入，
// 没有实际写入的分区不动（不会白白重新导出、重新上传）。
// 要写入的行落在已定稿分区时报错（整批回滚）：调用方必须先用 ReopenFinal 读回原文件，否则重新导出会只剩新行。
func (s *Store) Stage(ctx context.Context, task Task, rows []Row, partition func(time.Time) PartitionRef, now time.Time, mode StageMode) (StageResult, error) {
	var result StageResult
	if len(rows) == 0 {
		return result, nil
	}
	if err := task.Validate(); err != nil {
		return result, err
	}
	// 同一批里主键重复的行以最后一条为准（按纳秒比较，与暂存表的主键一致）。
	type key struct {
		subject string
		at      int64
		tag     string
	}
	index := make(map[key]int, len(rows))
	unique := make([]Row, 0, len(rows))
	for _, row := range rows {
		if len(row.Values) != len(task.Columns) {
			return result, fmt.Errorf("row %s@%s has %d values, want %d", row.SubjectID, row.DataTime.Format(time.RFC3339Nano), len(row.Values), len(task.Columns))
		}
		if err := CheckNanosRange(row.DataTime); err != nil {
			return result, fmt.Errorf("标的 %s 的 data_time: %w", row.SubjectID, err)
		}
		k := key{row.SubjectID, nanos(row.DataTime), row.SeriesTag}
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
		for _, ddl := range []string{`DROP TABLE IF EXISTS temp.incoming`, `DROP TABLE IF EXISTS temp.changed`} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		extra := ""
		if len(typedefs) > 0 {
			extra = ", " + strings.Join(typedefs, ", ")
		}
		if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE incoming (subject_id TEXT, data_time INTEGER, series_tag TEXT`+extra+`, pkey TEXT, pstart INTEGER, pend INTEGER)`); err != nil {
			return err
		}
		perRow := 6 + len(names)
		chunk := max(maxParams/perRow, 1)
		for start := 0; start < len(unique); start += chunk {
			end := min(start+chunk, len(unique))
			var placeholders []string
			var args []any
			for _, row := range unique[start:end] {
				ref := partition(row.DataTime)
				placeholders = append(placeholders, "("+strings.TrimSuffix(strings.Repeat("?,", perRow), ",")+")")
				args = append(args, row.SubjectID, nanos(row.DataTime), row.SeriesTag)
				for i, value := range row.Values {
					bound, err := bindValue(task.Columns[i].Type, value)
					if err != nil {
						return fmt.Errorf("column %s of %s@%s: %w", task.Columns[i].Name, row.SubjectID, row.DataTime.Format(time.RFC3339Nano), err)
					}
					args = append(args, bound)
				}
				args = append(args, ref.Key, nanos(ref.Start), nanos(ref.End))
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO incoming VALUES `+strings.Join(placeholders, ","), args...); err != nil {
				return fmt.Errorf("stage incoming rows: %w", err)
			}
		}
		var differs []string
		for _, c := range task.Columns {
			differs = append(differs, fmt.Sprintf("i.%s IS NOT s.%s", quote(c.Name), quote(c.Name)))
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
		write := ""
		if mode == StageFillGaps {
			write = " WHERE existed = 0"
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT OR REPLACE INTO %s (subject_id, data_time, series_tag%s) SELECT subject_id, data_time, series_tag%s FROM changed%s`, table, valueList, valueList, write)); err != nil {
			return fmt.Errorf("write changed rows: %w", err)
		}
		stats, err := tx.QueryContext(ctx, `SELECT pkey, pstart, pend, SUM(existed), SUM(1 - existed) FROM changed GROUP BY pkey, pstart, pend`)
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
			var pstart, pend, changed, added int64
			if err := stats.Scan(&t.ref.Key, &pstart, &pend, &changed, &added); err != nil {
				_ = stats.Close()
				return err
			}
			t.ref.Start, t.ref.End = fromNanos(pstart), fromNanos(pend)
			t.changed, t.added = uint64(changed), uint64(added)
			list = append(list, t)
		}
		if err := stats.Close(); err != nil {
			return err
		}
		// compared 是临时表 changed 的行数（新增 + 同键不同），其余输入行同键且值相同。
		var compared uint64
		for _, t := range list {
			compared += t.changed + t.added
			result.Changed += t.changed
			result.Added += t.added
			written := t.added
			if mode == StageReplace {
				written += t.changed
			}
			if written == 0 {
				continue
			}
			var status string
			err := tx.QueryRowContext(ctx, `SELECT status FROM partition_state WHERE space_id = ? AND dataset_id = ? AND partition_key = ?`, task.SpaceID, task.DatasetID, t.ref.Key).Scan(&status)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if status == StatusFinal {
				return fmt.Errorf("分区 %s 已定稿，写入前必须先读回原文件", t.ref.Key)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO partition_state (space_id, dataset_id, partition_key, range_start, range_end, status, last_changed_at)
VALUES (?, ?, ?, ?, ?, 'open', ?)
ON CONFLICT (space_id, dataset_id, partition_key) DO UPDATE SET last_changed_at = excluded.last_changed_at, report_pending = 1`,
				task.SpaceID, task.DatasetID, t.ref.Key, nanos(t.ref.Start), nanos(t.ref.End), nanos(now)); err != nil {
				return err
			}
		}
		result.Same = uint64(len(unique)) - compared
		for _, ddl := range []string{`DROP TABLE IF EXISTS temp.incoming`, `DROP TABLE IF EXISTS temp.changed`} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return StageResult{}, err
	}
	return result, nil
}
