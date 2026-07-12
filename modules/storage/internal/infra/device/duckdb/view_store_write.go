//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factkey"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Options 保存 DuckDB 视图存储打开配置。

func (s *ViewStore) Write(ctx context.Context, indexID string, batch viewindex.ViewIndexBatch) error {
	if len(batch.RecordRows) > 0 {
		return fmt.Errorf("duckdb view index rejects record rows")
	}
	return s.InsertRows(ctx, indexID, batch.TimeSeriesRows)
}

func (s *ViewStore) InsertRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	columns, err := s.loadColumns(ctx, tableName)
	if err != nil {
		return err
	}
	empty, err := s.resultTableEmpty(ctx, quoted)
	if err != nil {
		return err
	}
	if empty {
		_, err = s.insertRowsIntoEmptyTable(ctx, quoted, columns, rows)
	} else {
		_, err = s.mergeRowsIntoTable(ctx, quoted, columns, rows)
	}
	return err
}

func (s *ViewStore) resultTableEmpty(ctx context.Context, quotedTableName string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT 1 FROM %s LIMIT 1`, quotedTableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return !rows.Next(), rows.Err()
}

func (s *ViewStore) indexMeta(ctx context.Context, tableName string) (persistedIndexMeta, error) {
	var meta persistedIndexMeta
	err := s.db.QueryRowContext(ctx, `
		SELECT view_version, entry_count, min_version, max_version, schema_hash, updated_at
		FROM moox_view_index_meta WHERE table_name = ?
	`, tableName).Scan(&meta.viewVersion, &meta.entryCount, &meta.minVersion, &meta.maxVersion, &meta.schemaHash, &meta.updatedAt)
	return meta, err
}

func updateIndexMetaTx(ctx context.Context, tx *sql.Tx, tableName string, delta int64, minVersion string, maxVersion string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO moox_view_index_meta (table_name, entry_count, min_version, max_version, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(table_name) DO UPDATE SET
			entry_count = moox_view_index_meta.entry_count + excluded.entry_count,
			min_version = CASE
				WHEN moox_view_index_meta.min_version = '' THEN excluded.min_version
				WHEN excluded.min_version = '' THEN moox_view_index_meta.min_version
				WHEN excluded.min_version < moox_view_index_meta.min_version THEN excluded.min_version
				ELSE moox_view_index_meta.min_version END,
			max_version = CASE
				WHEN moox_view_index_meta.max_version = '' THEN excluded.max_version
				WHEN excluded.max_version = '' THEN moox_view_index_meta.max_version
				WHEN excluded.max_version > moox_view_index_meta.max_version THEN excluded.max_version
				ELSE moox_view_index_meta.max_version END,
			updated_at = excluded.updated_at
	`, tableName, delta, minVersion, maxVersion, now)
	return err
}

func timeSeriesRowsVersionBounds(rows []*pb.TimeSeriesRow) (string, string) {
	var minVersion, maxVersion string
	for _, row := range rows {
		version := normalizeRowDataTime(row)
		if version == "" {
			continue
		}
		if minVersion == "" || version < minVersion {
			minVersion = version
		}
		if maxVersion == "" || version > maxVersion {
			maxVersion = version
		}
	}
	return minVersion, maxVersion
}

func mergeRowsByPrimaryKey(rows []*pb.TimeSeriesRow) []*pb.TimeSeriesRow {
	positions := make(map[string]int, len(rows))
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		key := timeSeriesRowKey(row) + "|" + normalizeRowDataTime(row)
		if idx, ok := positions[key]; ok {
			out[idx] = mergeTimeSeriesRow(out[idx], row)
			continue
		}
		positions[key] = len(out)
		out = append(out, normalizeTimeSeriesRow(row))
	}
	return out
}

func (s *ViewStore) mergeRowsIntoTable(ctx context.Context, quotedTableName string, columns []*pb.ResultColumn, rows []*pb.TimeSeriesRow) ([]*pb.TimeSeriesRow, error) {
	mergedRows := mergeRowsByPrimaryKey(rows)
	if len(mergedRows) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	existing, err := loadExistingRows(ctx, tx, quotedTableName, mergedRows)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := deleteRowsByPrimaryKey(ctx, tx, quotedTableName, mergedRows); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	insertSQL, err := buildInsertSQL(quotedTableName, columns)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	insertStmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	defer insertStmt.Close()
	syncedRows := make([]*pb.TimeSeriesRow, 0, len(mergedRows))
	for _, row := range mergedRows {
		merged := row
		if existingRaw := existing[rowPrimaryKey(row)]; existingRaw != "" {
			base := &pb.TimeSeriesRow{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(existingRaw), base); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			merged = mergeTimeSeriesRow(base, row)
		}
		args, err := resultRowArgs(merged, columns)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if _, err := insertStmt.ExecContext(ctx, args...); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		syncedRows = append(syncedRows, normalizeTimeSeriesRow(merged))
	}
	newRows := int64(len(mergedRows) - len(existing))
	minVersion, maxVersion := timeSeriesRowsVersionBounds(syncedRows)
	tableName, err := unquoteTableName(quotedTableName)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := updateIndexMetaTx(ctx, tx, tableName, newRows, minVersion, maxVersion); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return syncedRows, nil
}

func loadExistingRows(ctx context.Context, tx *sql.Tx, quotedTableName string, rows []*pb.TimeSeriesRow) (map[string]string, error) {
	out := make(map[string]string, len(rows))
	for start := 0; start < len(rows); start += rowKeyPredicateChunkSize {
		end := start + rowKeyPredicateChunkSize
		if end > len(rows) {
			end = len(rows)
		}
		where, args := rowKeyPredicate(rows[start:end])
		if where == "" {
			continue
		}
		query := fmt.Sprintf(`SELECT row_key, data_time, row_json FROM %s WHERE %s`, quotedTableName, where)
		resultRows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for resultRows.Next() {
			var rowKey, dataTime, raw string
			if err := resultRows.Scan(&rowKey, &dataTime, &raw); err != nil {
				_ = resultRows.Close()
				return nil, err
			}
			out[rowKey+"|"+dataTime] = raw
		}
		if err := resultRows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func deleteRowsByPrimaryKey(ctx context.Context, tx *sql.Tx, quotedTableName string, rows []*pb.TimeSeriesRow) error {
	for start := 0; start < len(rows); start += rowKeyPredicateChunkSize {
		end := start + rowKeyPredicateChunkSize
		if end > len(rows) {
			end = len(rows)
		}
		where, args := rowKeyPredicate(rows[start:end])
		if where == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s`, quotedTableName, where), args...); err != nil {
			return err
		}
	}
	return nil
}

func rowPrimaryKey(row *pb.TimeSeriesRow) string {
	return timeSeriesRowKey(row) + "|" + normalizeRowDataTime(row)
}

func mergeTimeSeriesRow(base *pb.TimeSeriesRow, patch *pb.TimeSeriesRow) *pb.TimeSeriesRow {
	if base == nil {
		return normalizeTimeSeriesRow(patch)
	}
	merged := proto.Clone(base).(*pb.TimeSeriesRow)
	merged.Key = normalizeTimeSeriesRow(patch).GetKey()
	positions := make(map[string]int, len(merged.GetColumns()))
	for idx, column := range merged.GetColumns() {
		positions[column.GetColumnName()] = idx
	}
	for _, column := range patch.GetColumns() {
		copied := proto.Clone(column).(*pb.ColumnValue)
		if idx, ok := positions[column.GetColumnName()]; ok {
			if isNullColumn(copied) && !isNullColumn(merged.Columns[idx]) {
				continue
			}
			merged.Columns[idx] = copied
			continue
		}
		positions[column.GetColumnName()] = len(merged.Columns)
		merged.Columns = append(merged.Columns, copied)
	}
	return merged
}

func isNullColumn(column *pb.ColumnValue) bool {
	return column == nil || column.GetValue() == nil
}

func buildInsertSQL(quotedTableName string, columns []*pb.ResultColumn) (string, error) {
	names := append([]string{}, resultBaseColumns...)
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			continue
		}
		if _, err := quoteColumnName(name); err != nil {
			return "", err
		}
		names = append(names, name)
	}
	quotedNames := make([]string, 0, len(names))
	placeholders := make([]string, 0, len(names))
	for _, name := range names {
		quotedName, err := quoteColumnName(name)
		if err != nil {
			return "", err
		}
		quotedNames = append(quotedNames, quotedName)
		placeholders = append(placeholders, "?")
	}
	return fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`, quotedTableName, strings.Join(quotedNames, ","), strings.Join(placeholders, ",")), nil
}

func resultRowArgs(row *pb.TimeSeriesRow, columns []*pb.ResultColumn) ([]any, error) {
	normalized := normalizeTimeSeriesRow(row)
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	dimensionsRaw, err := json.Marshal(normalized.GetKey().GetDimensions())
	if err != nil {
		return nil, err
	}
	attributesRaw, err := json.Marshal(normalized.GetAttributes())
	if err != nil {
		return nil, err
	}
	values := make(map[string]*pb.ColumnValue, len(normalized.GetColumns()))
	for _, column := range normalized.GetColumns() {
		values[column.GetColumnName()] = column
	}
	key := normalized.GetKey()
	args := []any{
		timeSeriesRowKey(normalized),
		key.GetSpaceId(),
		key.GetDatasetId(),
		key.GetSubjectId(),
		key.GetFreq(),
		string(dimensionsRaw),
		key.GetDataTime(),
		string(attributesRaw),
		string(raw),
	}
	for _, column := range columns {
		args = append(args, sqlValue(values[column.GetColumnName()], column.GetValueType()))
	}
	return args, nil
}

func normalizeTimeSeriesRow(row *pb.TimeSeriesRow) *pb.TimeSeriesRow {
	if row == nil {
		return nil
	}
	out := proto.Clone(row).(*pb.TimeSeriesRow)
	if out.Key == nil {
		out.Key = &pb.TimeSeriesKey{}
	}
	if normalized, err := factkey.NormalizeTimeVersion(out.GetKey().GetDataTime()); err == nil {
		out.Key.DataTime = normalized
	}
	return out
}

func normalizeRowDataTime(row *pb.TimeSeriesRow) string {
	if row == nil || row.GetKey() == nil {
		return ""
	}
	if normalized, err := factkey.NormalizeTimeVersion(row.GetKey().GetDataTime()); err == nil {
		return normalized
	}
	return row.GetKey().GetDataTime()
}

func sqlValue(column *pb.ColumnValue, valueType pb.FieldValueType) any {
	if column == nil || column.GetValue() == nil {
		return nil
	}
	value := column.GetValue()
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		if _, ok := value.GetValue().(*pb.TypedValue_IntValue); ok {
			return value.GetIntValue()
		}
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		if _, ok := value.GetValue().(*pb.TypedValue_DoubleValue); ok {
			return value.GetDoubleValue()
		}
		if number, ok := factvalue.Numeric(value); ok {
			return number
		}
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		if _, ok := value.GetValue().(*pb.TypedValue_BoolValue); ok {
			return value.GetBoolValue()
		}
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		if normalized, err := factkey.NormalizeTimeVersion(value.GetTimeValue()); err == nil {
			return normalized
		}
		return value.GetTimeValue()
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return value.GetJsonValue()
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return value.GetBytesValue()
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING, pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED:
		return factvalue.String(value)
	}
	return factvalue.String(value)
}
