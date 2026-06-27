//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Options 保存 DuckDB 视图存储打开配置。
type Options struct {
	Path string
}

// ViewStore 封装 TimeSeries 视图在 DuckDB 中的物化读写能力。
type ViewStore struct {
	db         *sql.DB
	tableLocks sync.Map
}

var tableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var unsafeIndexNameChar = regexp.MustCompile(`[^A-Za-z0-9_]+`)

var resultBaseColumns = []string{
	"row_key",
	"space_id",
	"dataset_id",
	"subject_id",
	"freq",
	"dimensions_json",
	"data_time",
	"attributes_json",
	"row_json",
}

func Open(opts Options) (*ViewStore, error) {
	if opts.Path == "" {
		return nil, errors.New("duckdb path is required")
	}
	db, err := sql.Open("duckdb", opts.Path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &ViewStore{db: db}
	if err := store.init(trpc.BackgroundContext()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *ViewStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *ViewStore) init(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS moox_view_columns (
			table_name VARCHAR PRIMARY KEY,
			columns_json VARCHAR NOT NULL
		)
	`)
	return err
}

func (s *ViewStore) CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	columnDefs, err := resultColumnDefs(columns)
	if err != nil {
		return err
	}
	unlock := s.lockResultTable(tableName)
	defer unlock()
	defs := []string{
		"row_key VARCHAR NOT NULL",
		"space_id VARCHAR NOT NULL",
		"dataset_id VARCHAR NOT NULL",
		"subject_id VARCHAR NOT NULL",
		"freq VARCHAR NOT NULL",
		"dimensions_json VARCHAR NOT NULL",
		"data_time VARCHAR NOT NULL",
		"attributes_json VARCHAR NOT NULL",
		"row_json VARCHAR NOT NULL",
	}
	for _, def := range columnDefs {
		defs = append(defs, quoteColumnNameMust(def.name)+" "+duckDBType(def.valueType))
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			%s
		)
	`, quoted, strings.Join(defs, ",\n\t\t\t"))); err != nil {
		return err
	}
	if err := s.createResultIndexes(ctx, tableName, columnDefs); err != nil {
		return err
	}
	encoded, err := encodeColumns(columns)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO moox_view_columns (table_name, columns_json)
		VALUES (?, ?)
		ON CONFLICT(table_name) DO UPDATE SET columns_json = excluded.columns_json
	`, tableName, encoded)
	return err
}

type resultColumnDef struct {
	name      string
	valueType pb.FieldValueType
}

func resultColumnDefs(columns []*pb.ViewColumn) ([]resultColumnDef, error) {
	seen := make(map[string]bool, len(columns))
	out := make([]resultColumnDef, 0, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			return nil, errors.New("view column_name is required")
		}
		if _, err := quoteColumnName(name); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, resultColumnDef{name: name, valueType: column.GetValueType()})
	}
	return out, nil
}

func resultColumnDefsFromResultColumns(columns []*pb.ResultColumn) ([]resultColumnDef, error) {
	seen := make(map[string]bool, len(columns))
	out := make([]resultColumnDef, 0, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			return nil, errors.New("view column_name is required")
		}
		if _, err := quoteColumnName(name); err != nil {
			return nil, err
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, resultColumnDef{name: name, valueType: column.GetValueType()})
	}
	return out, nil
}

func (s *ViewStore) createResultIndexes(ctx context.Context, tableName string, columns []resultColumnDef) error {
	statements, err := createResultIndexStatements(tableName, columns)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func createResultIndexStatements(tableName string, columns []resultColumnDef) ([]string, error) {
	quotedTable, err := quoteTableName(tableName)
	if err != nil {
		return nil, err
	}
	statements := []string{
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (row_key, data_time)`, quoteIndexNameMust(tableName, "key_time"), quotedTable),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (subject_id, freq, data_time)`, quoteIndexNameMust(tableName, "subject_freq_time"), quotedTable),
	}
	for _, column := range columns {
		statements = append(statements, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (%s)`,
			quoteIndexNameMust(tableName, column.name),
			quotedTable,
			quoteColumnNameMust(column.name),
		))
	}
	return statements, nil
}

func dropResultIndexStatements(tableName string, columns []resultColumnDef) []string {
	indexNames := []string{
		quoteIndexNameMust(tableName, "key_time"),
		quoteIndexNameMust(tableName, "subject_freq_time"),
	}
	for _, column := range columns {
		indexNames = append(indexNames, quoteIndexNameMust(tableName, column.name))
	}
	statements := make([]string, 0, len(indexNames))
	for _, indexName := range indexNames {
		statements = append(statements, fmt.Sprintf(`DROP INDEX IF EXISTS %s`, indexName))
	}
	return statements
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
	columns, err := s.loadColumns(ctx, tableName)
	if err != nil {
		return err
	}
	empty, err := s.resultTableEmpty(ctx, quoted)
	if err != nil {
		return err
	}
	if empty {
		return s.insertRowsIntoEmptyTable(ctx, quoted, columns, rows)
	}
	return s.mergeRowsIntoTable(ctx, quoted, columns, rows)
}

func (s *ViewStore) lockResultTable(tableName string) func() {
	actual, _ := s.tableLocks.LoadOrStore(tableName, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *ViewStore) resultTableEmpty(ctx context.Context, quotedTableName string) (bool, error) {
	var count uint64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quotedTableName)).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *ViewStore) insertRowsIntoEmptyTable(ctx context.Context, quotedTableName string, columns []*pb.ResultColumn, rows []*pb.TimeSeriesRow) error {
	merged := mergeRowsByPrimaryKey(rows)
	tableName, err := unquoteTableName(quotedTableName)
	if err != nil {
		return err
	}
	columnDefs, err := resultColumnDefsFromResultColumns(columns)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, statement := range dropResultIndexStatements(tableName, columnDefs) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	insertSQL, err := buildInsertSQL(quotedTableName, columns)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	insertStmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer insertStmt.Close()
	for _, row := range merged {
		args, err := resultRowArgs(row, columns)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := insertStmt.ExecContext(ctx, args...); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	indexStatements, err := createResultIndexStatements(tableName, columnDefs)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, statement := range indexStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
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

func (s *ViewStore) mergeRowsIntoTable(ctx context.Context, quotedTableName string, columns []*pb.ResultColumn, rows []*pb.TimeSeriesRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	selectStmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`SELECT row_json FROM %s WHERE row_key = ? AND data_time = ? LIMIT 1`, quotedTableName))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer selectStmt.Close()
	deleteStmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE row_key = ? AND data_time = ?`, quotedTableName))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer deleteStmt.Close()
	insertSQL, err := buildInsertSQL(quotedTableName, columns)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	insertStmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer insertStmt.Close()
	for _, row := range rows {
		rowKey := timeSeriesRowKey(row)
		dataTime := normalizeRowDataTime(row)
		merged := row
		var existingRaw string
		if err := selectStmt.QueryRowContext(ctx, rowKey, dataTime).Scan(&existingRaw); err == nil {
			existing := &pb.TimeSeriesRow{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(existingRaw), existing); err != nil {
				_ = tx.Rollback()
				return err
			}
			merged = mergeTimeSeriesRow(existing, row)
		} else if !errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return err
		}
		args, err := resultRowArgs(merged, columns)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := deleteStmt.ExecContext(ctx, rowKey, dataTime); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := insertStmt.ExecContext(ctx, args...); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func timeSeriesRowKey(row *pb.TimeSeriesRow) string {
	key := row.GetKey()
	return key.GetDatasetId() + "|" + factkey.BuildTimeSeriesDataKey(key.GetSubjectId(), key.GetFreq(), key.GetDimensions())
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
		quotedNames = append(quotedNames, quoteColumnNameMust(name))
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

func (s *ViewStore) ListResultTables(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_name LIKE 'ts_view_%' OR table_name LIKE 'view_result_%'
		ORDER BY table_name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return nil, err
		}
		out = append(out, tableName)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ViewStore) DropResultTable(ctx context.Context, tableName string) error {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, quoted)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM moox_view_columns WHERE table_name = ?`, tableName); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *ViewStore) QueryTimeSeriesRows(ctx context.Context, tableName string, req *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return nil, nil, nil, err
	}
	columns, err := s.loadColumns(ctx, tableName)
	if err != nil {
		return nil, nil, nil, err
	}

	sqlText, countSQL, args, pageNo, size, err := buildTimeSeriesQuery(quoted, columns, req)
	if err != nil {
		return nil, nil, nil, err
	}
	var total uint64
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	out, err := scanResultRows(rows, columns)
	if err != nil {
		return nil, nil, nil, err
	}
	projectedColumns := projectColumns(columns, req.GetColumnNames())
	projectedRows := projectRows(out, req.GetColumnNames())
	return projectedColumns, projectedRows, &pb.PageResult{
		Page:    pageNo,
		Size:    size,
		Total:   uint32(total),
		HasMore: uint64(pageNo*size) < total,
	}, nil
}

func buildTimeSeriesQuery(quotedTableName string, columns []*pb.ResultColumn, req *pb.QueryTimeSeriesRowsReq) (string, string, []any, uint32, uint32, error) {
	where, args, err := buildSQLPredicates(req, columns)
	if err != nil {
		return "", "", nil, 0, 0, err
	}
	selectColumns, err := resultSelectColumns(columns)
	if err != nil {
		return "", "", nil, 0, 0, err
	}
	orderBy, err := buildOrderBy(req.GetSorts(), columns)
	if err != nil {
		return "", "", nil, 0, 0, err
	}
	pageNo, size := normalizePage(req.GetPage())
	offset := (pageNo - 1) * size
	sqlText := fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(selectColumns, ","), quotedTableName)
	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quotedTableName)
	if where != "" {
		sqlText += " WHERE " + where
		countSQL += " WHERE " + where
	}
	sqlText += " ORDER BY " + orderBy
	sqlText += fmt.Sprintf(" LIMIT %d OFFSET %d", size, offset)
	return sqlText, countSQL, args, pageNo, size, nil
}

func buildSQLPredicates(req *pb.QueryTimeSeriesRowsReq, columns []*pb.ResultColumn) (string, []any, error) {
	var clauses []string
	var args []any
	if keyClause, keyArgs, err := buildKeyPredicates(req.GetKeys()); err != nil {
		return "", nil, err
	} else if keyClause != "" {
		clauses = append(clauses, keyClause)
		args = append(args, keyArgs...)
	}
	if timeRange := req.GetTimeRange(); timeRange != nil {
		if start := strings.TrimSpace(timeRange.GetStartTime()); start != "" {
			normalized, err := factkey.NormalizeTimeVersion(start)
			if err != nil {
				return "", nil, errors.New("start_time must be RFC3339/RFC3339Nano")
			}
			clauses = append(clauses, "data_time >= ?")
			args = append(args, normalized)
		}
		if end := strings.TrimSpace(timeRange.GetEndTime()); end != "" {
			normalized, err := factkey.NormalizeTimeVersion(end)
			if err != nil {
				return "", nil, errors.New("end_time must be RFC3339/RFC3339Nano")
			}
			clauses = append(clauses, "data_time <= ?")
			args = append(args, normalized)
		}
	}
	filterClauses, filterArgs, err := buildFilterPredicates(req.GetFilters(), columns)
	if err != nil {
		return "", nil, err
	}
	clauses = append(clauses, filterClauses...)
	args = append(args, filterArgs...)
	return strings.Join(clauses, " AND "), args, nil
}

func buildKeyPredicates(keys []*pb.TimeSeriesKey) (string, []any, error) {
	var clauses []string
	var args []any
	for _, key := range keys {
		if key == nil {
			continue
		}
		var parts []string
		addString := func(column string, value string) {
			if strings.TrimSpace(value) == "" {
				return
			}
			parts = append(parts, quoteColumnNameMust(column)+" = ?")
			args = append(args, value)
		}
		addString("space_id", key.GetSpaceId())
		addString("dataset_id", key.GetDatasetId())
		addString("subject_id", key.GetSubjectId())
		addString("freq", key.GetFreq())
		if len(key.GetDimensions()) > 0 {
			raw, err := json.Marshal(key.GetDimensions())
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, "dimensions_json = ?")
			args = append(args, string(raw))
		}
		if dataTime := strings.TrimSpace(key.GetDataTime()); dataTime != "" {
			normalized, err := factkey.NormalizeTimeVersion(dataTime)
			if err != nil {
				return "", nil, errors.New("data_time must be RFC3339/RFC3339Nano")
			}
			parts = append(parts, "data_time = ?")
			args = append(args, normalized)
		}
		if len(parts) > 0 {
			clauses = append(clauses, "("+strings.Join(parts, " AND ")+")")
		}
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args, nil
}

func buildFilterPredicates(filters []*pb.FilterExpr, columns []*pb.ResultColumn) ([]string, []any, error) {
	if len(filters) == 0 {
		return nil, nil, nil
	}
	columnTypes := resultColumnTypes(columns)
	var clauses []string
	var args []any
	for _, filter := range filters {
		if filter == nil || strings.TrimSpace(filter.GetExpr()) == "" {
			continue
		}
		if fn, field, token, ok := parseFunctionFilter(filter.GetExpr()); ok {
			if _, ok := columnTypes[field]; !ok {
				return nil, nil, fmt.Errorf("unsupported filter field %q", field)
			}
			quoted := quoteColumnNameMust(field)
			switch fn {
			case "is_empty":
				clauses = append(clauses, fmt.Sprintf("(%s IS NULL OR CAST(%s AS VARCHAR) = '')", quoted, quoted))
				continue
			case "is_not_empty":
				clauses = append(clauses, fmt.Sprintf("(%s IS NOT NULL AND CAST(%s AS VARCHAR) <> '')", quoted, quoted))
				continue
			}
			value := filterValue(token, filter.GetArgs())
			if value == nil {
				return nil, nil, fmt.Errorf("unsupported filter value %q", token)
			}
			textValue := factvalue.String(value)
			switch fn {
			case "starts_with":
				clauses = append(clauses, fmt.Sprintf("CAST(%s AS VARCHAR) LIKE ?", quoted))
				args = append(args, textValue+"%")
				continue
			case "ends_with":
				clauses = append(clauses, fmt.Sprintf("CAST(%s AS VARCHAR) LIKE ?", quoted))
				args = append(args, "%"+textValue)
				continue
			case "not_contains":
				clauses = append(clauses, fmt.Sprintf("(%s IS NULL OR CAST(%s AS VARCHAR) NOT LIKE ?)", quoted, quoted))
				args = append(args, "%"+textValue+"%")
				continue
			default:
				return nil, nil, fmt.Errorf("unsupported filter expression %q", filter.GetExpr())
			}
		}
		left, op, right, ok := parseSimpleFilter(filter.GetExpr())
		if !ok {
			return nil, nil, fmt.Errorf("unsupported filter expression %q", filter.GetExpr())
		}
		valueType, ok := columnTypes[left]
		if !ok {
			return nil, nil, fmt.Errorf("unsupported filter field %q", left)
		}
		value := filterValue(right, filter.GetArgs())
		if value == nil {
			return nil, nil, fmt.Errorf("unsupported filter value %q", right)
		}
		quoted := quoteColumnNameMust(left)
		if op == "contains" {
			clauses = append(clauses, fmt.Sprintf("CAST(%s AS VARCHAR) LIKE ?", quoted))
			args = append(args, "%"+factvalue.String(value)+"%")
			continue
		}
		sqlOp := op
		if sqlOp == "==" {
			sqlOp = "="
		}
		if sqlOp == "!=" {
			sqlOp = "<>"
		}
		clauses = append(clauses, fmt.Sprintf("%s %s ?", quoted, sqlOp))
		args = append(args, typedSQLValue(value, valueType))
	}
	return clauses, args, nil
}

func buildOrderBy(sorts []*pb.SortSpec, columns []*pb.ResultColumn) (string, error) {
	if len(sorts) == 0 {
		return "subject_id ASC, freq ASC, data_time ASC", nil
	}
	columnTypes := resultColumnTypes(columns)
	parts := make([]string, 0, len(sorts)+3)
	for _, spec := range sorts {
		fieldName := strings.TrimSpace(spec.GetFieldName())
		if _, ok := columnTypes[fieldName]; !ok {
			return "", fmt.Errorf("unsupported sort field %q", fieldName)
		}
		direction := "ASC"
		if spec.GetDesc() {
			direction = "DESC"
		}
		parts = append(parts, quoteColumnNameMust(fieldName)+" "+direction)
	}
	parts = append(parts, "subject_id ASC", "freq ASC", "data_time ASC")
	return strings.Join(parts, ", "), nil
}

func resultSelectColumns(columns []*pb.ResultColumn) ([]string, error) {
	names := []string{"space_id", "dataset_id", "subject_id", "freq", "dimensions_json", "data_time", "attributes_json"}
	for _, column := range columns {
		if column.GetColumnName() == "" {
			continue
		}
		if _, err := quoteColumnName(column.GetColumnName()); err != nil {
			return nil, err
		}
		names = append(names, column.GetColumnName())
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, quoteColumnNameMust(name))
	}
	return out, nil
}

func resultColumnTypes(columns []*pb.ResultColumn) map[string]pb.FieldValueType {
	out := map[string]pb.FieldValueType{
		"space_id":   pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"dataset_id": pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"subject_id": pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"freq":       pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"data_time":  pb.FieldValueType_FIELD_VALUE_TYPE_TIME,
	}
	for _, column := range columns {
		out[column.GetColumnName()] = column.GetValueType()
	}
	return out
}

func normalizePage(page *pb.Page) (uint32, uint32) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	return pageNo, size
}

func scanResultRows(rows *sql.Rows, columns []*pb.ResultColumn) ([]*pb.TimeSeriesRow, error) {
	var out []*pb.TimeSeriesRow
	for rows.Next() {
		values := make([]any, 7+len(columns))
		dest := make([]any, len(values))
		for idx := range values {
			dest[idx] = &values[idx]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		dimensions := map[string]string{}
		if raw := dbString(values[4]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &dimensions); err != nil {
				return nil, err
			}
		}
		attributes := map[string]string{}
		if raw := dbString(values[6]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &attributes); err != nil {
				return nil, err
			}
		}
		row := &pb.TimeSeriesRow{
			Key: &pb.TimeSeriesKey{
				SpaceId:    dbString(values[0]),
				DatasetId:  dbString(values[1]),
				SubjectId:  dbString(values[2]),
				Freq:       dbString(values[3]),
				Dimensions: dimensions,
				DataTime:   dbString(values[5]),
			},
			Attributes: attributes,
			Columns:    make([]*pb.ColumnValue, 0, len(columns)),
		}
		for idx, column := range columns {
			row.Columns = append(row.Columns, &pb.ColumnValue{
				ColumnName: column.GetColumnName(),
				ValueType:  column.GetValueType(),
				Value:      typedValueFromDB(values[7+idx], column.GetValueType()),
			})
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func typedSQLValue(value *pb.TypedValue, valueType pb.FieldValueType) any {
	if value == nil {
		return nil
	}
	return sqlValue(&pb.ColumnValue{ValueType: valueType, Value: value}, valueType)
}

func typedValueFromDB(value any, valueType pb.FieldValueType) *pb.TypedValue {
	if value == nil {
		return nil
	}
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: dbInt(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: dbFloat(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: dbBool(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: dbString(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: dbString(value)}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		if bytes, ok := value.([]byte); ok {
			return &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: bytes}}
		}
		return &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: []byte(dbString(value))}}
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING, pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED:
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: dbString(value)}}
	default:
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: dbString(value)}}
	}
}

func dbString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

func dbInt(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		n, _ := strconv.ParseInt(string(v), 10, 64)
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	default:
		return 0
	}
}

func dbFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case []byte:
		n, _ := strconv.ParseFloat(string(v), 64)
		return n
	case string:
		n, _ := strconv.ParseFloat(v, 64)
		return n
	default:
		return 0
	}
}

func dbBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case []byte:
		b, _ := strconv.ParseBool(string(v))
		return b
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	default:
		return false
	}
}

func (s *ViewStore) loadColumns(ctx context.Context, tableName string) ([]*pb.ResultColumn, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT columns_json FROM moox_view_columns WHERE table_name = ?`, tableName).Scan(&raw); err != nil {
		return nil, err
	}
	rsp := &pb.QueryTimeSeriesRowsRsp{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), rsp); err != nil {
		return nil, err
	}
	return rsp.GetColumns(), nil
}

func encodeColumns(columns []*pb.ViewColumn) (string, error) {
	out := make([]*pb.ResultColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, &pb.ResultColumn{
			ColumnName: column.GetColumnName(),
			OriginType: column.GetOriginType(),
			OriginId:   column.GetOriginId(),
			ValueType:  column.GetValueType(),
		})
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&pb.QueryTimeSeriesRowsRsp{Columns: out})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func quoteTableName(tableName string) (string, error) {
	if !tableNamePattern.MatchString(tableName) {
		return "", fmt.Errorf("invalid duckdb table name %s", tableName)
	}
	return `"` + tableName + `"`, nil
}

func unquoteTableName(quotedTableName string) (string, error) {
	tableName := strings.Trim(quotedTableName, `"`)
	if !tableNamePattern.MatchString(tableName) {
		return "", fmt.Errorf("invalid duckdb table name %s", quotedTableName)
	}
	return tableName, nil
}

func quoteColumnName(columnName string) (string, error) {
	columnName = strings.TrimSpace(columnName)
	if columnName == "" || strings.Contains(columnName, `"`) || strings.ContainsAny(columnName, "\x00\r\n\t") {
		return "", fmt.Errorf("invalid duckdb column name %s", columnName)
	}
	return `"` + columnName + `"`, nil
}

func quoteColumnNameMust(columnName string) string {
	quoted, err := quoteColumnName(columnName)
	if err != nil {
		panic(err)
	}
	return quoted
}

func quoteIndexNameMust(tableName string, suffix string) string {
	name := "idx_" + tableName + "_" + safeIndexNamePart(suffix)
	if !tableNamePattern.MatchString(name) {
		panic(fmt.Sprintf("invalid duckdb index name %s", name))
	}
	return `"` + name + `"`
}

func safeIndexNamePart(value string) string {
	value = unsafeIndexNameChar.ReplaceAllString(strings.TrimSpace(value), "_")
	value = strings.Trim(value, "_")
	if value == "" {
		return "column"
	}
	if first := value[0]; (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') && first != '_' {
		value = "_" + value
	}
	return value
}

func duckDBType(valueType pb.FieldValueType) string {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return "BIGINT"
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return "DOUBLE"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return "BOOLEAN"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return "BLOB"
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		pb.FieldValueType_FIELD_VALUE_TYPE_TIME,
		pb.FieldValueType_FIELD_VALUE_TYPE_JSON,
		pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED:
		return "VARCHAR"
	default:
		return "VARCHAR"
	}
}

func stringSet(values []string) map[string]bool {
	return factvalue.StringSet(values)
}

func projectColumns(columns []*pb.ResultColumn, includes []string) []*pb.ResultColumn {
	if len(includes) == 0 {
		return columns
	}
	allow := stringSet(includes)
	out := make([]*pb.ResultColumn, 0, len(includes))
	for _, column := range columns {
		if allow[column.GetColumnName()] {
			out = append(out, column)
		}
	}
	return out
}

func projectRows(rows []*pb.TimeSeriesRow, includes []string) []*pb.TimeSeriesRow {
	if len(includes) == 0 {
		return rows
	}
	allow := stringSet(includes)
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		projected := &pb.TimeSeriesRow{
			Key:     row.GetKey(),
			Columns: make([]*pb.ColumnValue, 0, len(includes)),
		}
		for _, value := range row.GetColumns() {
			if allow[value.GetColumnName()] {
				projected.Columns = append(projected.Columns, value)
			}
		}
		out = append(out, projected)
	}
	return out
}
