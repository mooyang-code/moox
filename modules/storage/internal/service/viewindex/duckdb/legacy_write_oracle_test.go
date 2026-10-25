//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 本文件保留改造前的写入实现（每 32 行一条 UPSERT，另有一轮 view_rows/计数的去重插入），
// 仅作为按列批量写入的对照基准：同一批输入写进两个索引，表内容与计数必须完全一致。

const legacyMaxWriteRowsPerStatement = 32

// legacyWrite 是改造前 IndexManager.Write 中写行与计数的部分。
func legacyWrite(ctx context.Context, tx *sql.Tx, columns map[string]pb.FieldValueType, writes []viewindex.RowWrite, mode viewindex.WriteMode) error {
	writes = collapseRowWrites(writes)
	for _, group := range groupRowWrites(writes) {
		if err := legacyEnsureSeriesRowsAndCounts(ctx, tx, group.writes); err != nil {
			return err
		}
		names := append([]string{"subject_id", "freq", "data_time", "series_tag"}, group.columns...)
		names = append(names, internalAttributesColumn)
		for start := 0; start < len(group.writes); start += legacyMaxWriteRowsPerStatement {
			end := min(start+legacyMaxWriteRowsPerStatement, len(group.writes))
			chunk := group.writes[start:end]
			args := make([]any, 0, len(chunk)*len(names))
			for _, write := range chunk {
				rowValues, err := rowArgs(columns, names, write, mode)
				if err != nil {
					return err
				}
				args = append(args, rowValues...)
			}
			if _, err := tx.ExecContext(ctx, legacyUpsertSQLBatch(names, mode, len(chunk)), args...); err != nil {
				return err
			}
		}
	}
	return nil
}

func legacyEnsureSeriesRowsAndCounts(ctx context.Context, tx *sql.Tx, writes []viewindex.RowWrite) error {
	if len(writes) == 0 {
		return nil
	}
	type candidate struct {
		subject, freq, tag string
		when               time.Time
	}
	unique := make(map[string]candidate, len(writes))
	for _, write := range writes {
		key := write.Key.Key.GetTimeSeries()
		if key == nil {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, key.GetDataTime())
		if err != nil {
			return fmt.Errorf("invalid data_time: %w", err)
		}
		when = when.UTC()
		item := candidate{subject: key.GetSubjectId(), freq: key.GetFreq(), tag: key.GetSeriesTag(), when: when}
		unique[encodePhysicalKeyParts(item.subject, item.freq, canonicalCoverageTime(when), item.tag)] = item
	}
	items := make([]candidate, 0, len(unique))
	for _, item := range unique {
		items = append(items, item)
	}
	counts := make(map[string]struct {
		subject string
		freq    string
		tag     string
		count   uint64
	})
	for start := 0; start < len(items); start += legacyMaxWriteRowsPerStatement {
		end := min(start+legacyMaxWriteRowsPerStatement, len(items))
		chunk := items[start:end]
		placeholders := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*4)
		for _, item := range chunk {
			placeholders = append(placeholders, "(?, ?, ?, ?)")
			args = append(args, item.subject, item.freq, item.when, item.tag)
		}
		query := `INSERT INTO view_rows (subject_id, freq, data_time, series_tag) VALUES ` + strings.Join(placeholders, ",") + ` ON CONFLICT (subject_id, freq, data_time, series_tag) DO NOTHING RETURNING subject_id, freq, data_time, series_tag`
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var subject, freq, tag string
			var when time.Time
			if err := rows.Scan(&subject, &freq, &when, &tag); err != nil {
				_ = rows.Close()
				return err
			}
			countKey := subject + "\x00" + freq + "\x00" + tag
			entry := counts[countKey]
			entry.subject, entry.freq, entry.tag = subject, freq, tag
			entry.count++
			counts[countKey] = entry
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
	}
	countItems := make([]struct {
		subject string
		freq    string
		tag     string
		count   uint64
	}, 0, len(counts))
	for _, item := range counts {
		countItems = append(countItems, item)
	}
	for start := 0; start < len(countItems); start += legacyMaxWriteRowsPerStatement {
		end := min(start+legacyMaxWriteRowsPerStatement, len(countItems))
		chunk := countItems[start:end]
		values := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*4)
		for _, item := range chunk {
			values = append(values, "(?, ?, ?, ?)")
			args = append(args, item.subject, item.freq, item.tag, item.count)
		}
		query := `INSERT INTO view_series_counts (subject_id, freq, series_tag, row_count) VALUES ` + strings.Join(values, ",") + ` ON CONFLICT (subject_id, freq, series_tag) DO UPDATE SET row_count = view_series_counts.row_count + excluded.row_count`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}

func legacyUpsertSQLBatch(names []string, mode viewindex.WriteMode, rowCount int) string {
	if rowCount < 1 {
		rowCount = 1
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(names)), ",")
	values := make([]string, rowCount)
	for i := range values {
		values[i] = "(" + placeholders + ")"
	}
	sets := make([]string, 0, len(names)-4)
	for _, name := range names[4:] {
		// Live writes overwrite complete rows. Backfill only fills missing values.
		set := fmt.Sprintf("%s = excluded.%s", quote(name), quote(name))
		if name == internalAttributesColumn {
			// Dataset events often carry only a subset of provenance attributes.
			// Merge JSON objects by key so a later factor patch cannot erase hashes
			// written by earlier factors on the same projected row.
			set = fmt.Sprintf("%s = CASE WHEN excluded.%s IS NULL THEN view_rows.%s ELSE json_merge_patch(COALESCE(view_rows.%s, '{}'), excluded.%s) END", quote(name), quote(name), quote(name), quote(name), quote(name))
		}
		if mode == viewindex.Backfill {
			if name == internalAttributesColumn {
				// Backfill must never overwrite live provenance. Put the
				// backfill object first so existing/live keys win on conflicts.
				set = fmt.Sprintf("%s = CASE WHEN view_rows.%s IS NULL THEN excluded.%s ELSE json_merge_patch(COALESCE(excluded.%s, '{}'), view_rows.%s) END", quote(name), quote(name), quote(name), quote(name), quote(name))
			} else {
				set = fmt.Sprintf("%s = COALESCE(view_rows.%s, excluded.%s)", quote(name), quote(name), quote(name))
			}
		}
		sets = append(sets, set)
	}
	conflict := "ON CONFLICT (subject_id, freq, data_time, series_tag) DO NOTHING"
	if len(sets) > 0 {
		conflict = "ON CONFLICT (subject_id, freq, data_time, series_tag) DO UPDATE SET " + strings.Join(sets, ", ")
	}
	return fmt.Sprintf("INSERT INTO view_rows (%s) VALUES %s %s", joinQuoted(names), strings.Join(values, ","), conflict)
}
