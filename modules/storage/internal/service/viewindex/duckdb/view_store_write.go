//go:build legacy_viewindex && cgo

package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/rowkey"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/typedvalue"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Options 保存 DuckDB 视图存储打开配置。

func (s *ViewStore) Write(ctx context.Context, indexID string, batch viewindex.BatchWrite) error {
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
		if len(columns) > 1 {
			complete := make([]*pb.TimeSeriesRow, 0, len(rows))
			for _, row := range rows {
				if len(row.GetColumns()) >= len(columns) {
					complete = append(complete, row)
				}
			}
			rows = complete
		}
		_, err = s.insertRowsIntoEmptyTable(ctx, quoted, columns, rows)
	} else {
		_, err = s.mergeRowsIntoTable(ctx, quoted, columns, rows)
	}
	return err
}

func (s *ViewStore) DeleteRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	if len(rows) == 0 {
		return nil
	}
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	where, args := rowKeyPredicate(rows)
	if where == "" {
		_ = tx.Rollback()
		return nil
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s`, quoted, where), args...)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if deleted > 0 {
		name, err := unquoteTableName(quoted)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := updateIndexMetaTx(ctx, tx, name, -deleted, "", ""); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
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
	var checkpointsRaw string
	err := s.db.QueryRowContext(ctx, `
		SELECT view_version, entry_count, min_version, max_version, schema_hash, updated_at, indexed_from, indexed_to, checkpoints_json
		FROM moox_view_index_meta WHERE table_name = ?
	`, tableName).Scan(&meta.viewVersion, &meta.entryCount, &meta.minVersion, &meta.maxVersion, &meta.schemaHash, &meta.updatedAt, &meta.indexedFrom, &meta.indexedTo, &checkpointsRaw)
	if err == nil {
		meta.checkpoints = make(map[string]uint64)
		if checkpointsRaw != "" {
			err = json.Unmarshal([]byte(checkpointsRaw), &meta.checkpoints)
		}
	}
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
		if column == nil {
			continue
		}
		if idx, ok := positions[column.GetColumnName()]; ok && isStaleColumn(merged.Columns[idx], column) {
			continue
		}
		if isStaleColumnTombstone(merged.RemovedColumns, column) {
			continue
		}
		merged.RemovedColumns = removeColumnTombstone(merged.RemovedColumns, column.GetColumnName())
		copied := proto.Clone(column).(*pb.ColumnValue)
		if idx, ok := positions[column.GetColumnName()]; ok {
			merged.Columns[idx] = copied
			continue
		}
		positions[column.GetColumnName()] = len(merged.Columns)
		merged.Columns = append(merged.Columns, copied)
	}
	for _, name := range patch.GetRemovedColumnNames() {
		removal := &pb.ColumnRemoval{ColumnName: name, SourceShardId: patch.GetSourceShardId(), SourceSequence: patch.GetSourceSequence()}
		if isStaleColumnRemovalTombstone(merged.RemovedColumns, removal) {
			continue
		}
		if idx, ok := positions[name]; ok {
			if idx >= 0 && idx < len(merged.Columns) && isStaleColumnRemoval(merged.Columns[idx], removal) {
				continue
			}
			merged.Columns = append(merged.Columns[:idx], merged.Columns[idx+1:]...)
			positions = make(map[string]int, len(merged.Columns))
			for pos, value := range merged.Columns {
				positions[value.GetColumnName()] = pos
			}
		}
		merged.RemovedColumns = appendColumnTombstone(merged.RemovedColumns, removal)
	}
	for _, removal := range patch.GetRemovedColumns() {
		if removal == nil {
			continue
		}
		if isStaleColumnRemovalTombstone(merged.RemovedColumns, removal) {
			continue
		}
		if idx, ok := positions[removal.GetColumnName()]; ok {
			if isStaleColumnRemoval(merged.Columns[idx], removal) {
				continue
			}
			merged.Columns = append(merged.Columns[:idx], merged.Columns[idx+1:]...)
			positions = make(map[string]int, len(merged.Columns))
			for pos, value := range merged.Columns {
				positions[value.GetColumnName()] = pos
			}
		}
		merged.RemovedColumns = appendColumnTombstone(merged.RemovedColumns, removal)
	}
	if len(patch.GetAttributes()) > 0 {
		if merged.Attributes == nil {
			merged.Attributes = make(map[string]string, len(patch.GetAttributes()))
		}
		for key, value := range patch.GetAttributes() {
			merged.Attributes[key] = value
		}
	}
	for _, key := range patch.GetAttributesToDelete() {
		delete(merged.Attributes, key)
	}
	merged.SourceShardId = patch.GetSourceShardId()
	merged.SourceSequence = patch.GetSourceSequence()
	merged.RemovedColumnNames = nil
	return merged
}

func isStaleColumn(existing, incoming *pb.ColumnValue) bool {
	return existing != nil && incoming != nil && incoming.GetSourceShardId() != "" && incoming.GetSourceSequence() != 0 &&
		existing.GetSourceShardId() == incoming.GetSourceShardId() && existing.GetSourceSequence() >= incoming.GetSourceSequence()
}

func isStaleColumnRemoval(existing *pb.ColumnValue, incoming *pb.ColumnRemoval) bool {
	return existing != nil && incoming != nil && incoming.GetSourceShardId() != "" && incoming.GetSourceSequence() != 0 && existing.GetSourceShardId() == incoming.GetSourceShardId() && existing.GetSourceSequence() >= incoming.GetSourceSequence()
}

func isStaleColumnTombstone(existing []*pb.ColumnRemoval, incoming *pb.ColumnValue) bool {
	if incoming == nil || incoming.GetSourceShardId() == "" || incoming.GetSourceSequence() == 0 {
		return false
	}
	for _, removal := range existing {
		if removal != nil && removal.GetColumnName() == incoming.GetColumnName() && removal.GetSourceShardId() == incoming.GetSourceShardId() && removal.GetSourceSequence() >= incoming.GetSourceSequence() {
			return true
		}
	}
	return false
}

func isStaleColumnRemovalTombstone(existing []*pb.ColumnRemoval, incoming *pb.ColumnRemoval) bool {
	if incoming == nil || incoming.GetSourceShardId() == "" || incoming.GetSourceSequence() == 0 {
		return false
	}
	for _, removal := range existing {
		if removal != nil && removal.GetColumnName() == incoming.GetColumnName() && removal.GetSourceShardId() == incoming.GetSourceShardId() && removal.GetSourceSequence() >= incoming.GetSourceSequence() {
			return true
		}
	}
	return false
}

func appendColumnTombstone(values []*pb.ColumnRemoval, incoming *pb.ColumnRemoval) []*pb.ColumnRemoval {
	if incoming == nil || incoming.GetColumnName() == "" {
		return values
	}
	filtered := values[:0]
	for _, value := range values {
		if value != nil && value.GetColumnName() != incoming.GetColumnName() {
			filtered = append(filtered, value)
		}
	}
	return append(filtered, proto.Clone(incoming).(*pb.ColumnRemoval))
}

func removeColumnTombstone(values []*pb.ColumnRemoval, name string) []*pb.ColumnRemoval {
	filtered := values[:0]
	for _, value := range values {
		if value != nil && value.GetColumnName() != name {
			filtered = append(filtered, value)
		}
	}
	return filtered
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
	if normalized, err := rowkey.NormalizeTimeVersion(out.GetKey().GetDataTime()); err == nil {
		out.Key.DataTime = normalized
	}
	out.AttributesToDelete = nil
	return out
}

func normalizeRowDataTime(row *pb.TimeSeriesRow) string {
	if row == nil || row.GetKey() == nil {
		return ""
	}
	if normalized, err := rowkey.NormalizeTimeVersion(row.GetKey().GetDataTime()); err == nil {
		return normalized
	}
	return row.GetKey().GetDataTime()
}

func sqlValue(column *pb.ColumnValue, valueType pb.FieldValueType) any {
	if column == nil || column.GetValue() == nil {
		return nil
	}
	value := column.GetValue()
	if _, ok := value.GetValue().(*pb.TypedValue_NullValue); ok {
		return nil
	}
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		if _, ok := value.GetValue().(*pb.TypedValue_IntValue); ok {
			return value.GetIntValue()
		}
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		if _, ok := value.GetValue().(*pb.TypedValue_DoubleValue); ok {
			return value.GetDoubleValue()
		}
		if number, ok := typedvalue.Numeric(value); ok {
			return number
		}
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		if _, ok := value.GetValue().(*pb.TypedValue_BoolValue); ok {
			return value.GetBoolValue()
		}
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		if normalized, err := rowkey.NormalizeTimeVersion(value.GetTimeValue()); err == nil {
			return normalized
		}
		return value.GetTimeValue()
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return value.GetJsonValue()
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return value.GetBytesValue()
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING, pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED:
		return typedvalue.String(value)
	}
	return typedvalue.String(value)
}
