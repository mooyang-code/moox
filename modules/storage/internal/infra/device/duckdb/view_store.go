//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

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
	db *sql.DB
}

var tableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			row_key VARCHAR NOT NULL,
			subject_id VARCHAR NOT NULL,
			data_time VARCHAR NOT NULL,
			row_json VARCHAR NOT NULL
		)
	`, quoted)); err != nil {
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

func (s *ViewStore) InsertRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	selectStmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`SELECT row_json FROM %s WHERE row_key = ? AND data_time = ? LIMIT 1`, quoted))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer selectStmt.Close()
	deleteStmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE row_key = ? AND data_time = ?`, quoted))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer deleteStmt.Close()
	insertStmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`INSERT INTO %s (row_key, subject_id, data_time, row_json) VALUES (?, ?, ?, ?)`, quoted))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer insertStmt.Close()
	for _, row := range rows {
		rowKey := timeSeriesRowKey(row)
		merged := row
		var existingRaw string
		if err := selectStmt.QueryRowContext(ctx, rowKey, row.GetKey().GetDataTime()).Scan(&existingRaw); err == nil {
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
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(row)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		raw, err = protojson.MarshalOptions{UseProtoNames: true}.Marshal(merged)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := deleteStmt.ExecContext(ctx, rowKey, row.GetKey().GetDataTime()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := insertStmt.ExecContext(ctx, rowKey, merged.GetKey().GetSubjectId(), merged.GetKey().GetDataTime(), string(raw)); err != nil {
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
		return proto.Clone(patch).(*pb.TimeSeriesRow)
	}
	merged := proto.Clone(base).(*pb.TimeSeriesRow)
	merged.Key = proto.Clone(patch.GetKey()).(*pb.TimeSeriesKey)
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

	// 条件下推：subject_id / data_time 是结果表真实列，直接在 SQL 层 WHERE 过滤，
	// 避免把整表 row_json 全量拉到内存。row_json 内部列的 filter/sort 仍在内存完成。
	where, args := buildPushdownPredicates(req)
	sqlText := fmt.Sprintf(`SELECT row_json FROM %s`, quoted)
	if where != "" {
		sqlText += " WHERE " + where
	}
	sqlText += " ORDER BY subject_id, data_time"

	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()

	// 内存兜底过滤：覆盖未能下推到 SQL 的情况（如非 RFC3339 时间边界），
	// 与下推条件幂等，不会误删已通过 SQL 过滤的行。
	allowSubjects := querySubjectSet(req.GetKeys())
	var out []*pb.TimeSeriesRow
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, nil, nil, err
		}
		row := &pb.TimeSeriesRow{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), row); err != nil {
			return nil, nil, nil, err
		}
		if !timeSeriesRowMatchesQuery(row, req, allowSubjects) {
			continue
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	out, err = filterRows(out, req.GetFilters())
	if err != nil {
		return nil, nil, nil, err
	}
	sortRows(out, req.GetSorts())
	projectedColumns := projectColumns(columns, req.GetColumnNames())
	projectedRows := projectRows(out, req.GetColumnNames())
	paged, page := pageRows(projectedRows, req.GetPage())
	return projectedColumns, paged, page, nil
}

// buildPushdownPredicates 构造可下推到 DuckDB 的 WHERE 条件与参数。
// 仅下推 subject_id（IN 列表）。data_time 不下推：结果表以字符串存储时间，
// 跨时区（如 +08:00 与 Z）字符串比较不可靠，时间过滤统一交由内存按 RFC3339 解析处理。
func buildPushdownPredicates(req *pb.QueryTimeSeriesRowsReq) (string, []any) {
	var clauses []string
	var args []any
	if subjects := querySubjects(req.GetKeys()); len(subjects) > 0 {
		placeholders := make([]string, 0, len(subjects))
		for _, subject := range subjects {
			placeholders = append(placeholders, "?")
			args = append(args, subject)
		}
		clauses = append(clauses, "subject_id IN ("+strings.Join(placeholders, ",")+")")
	}
	return strings.Join(clauses, " AND "), args
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

func stringSet(values []string) map[string]bool {
	return factvalue.StringSet(values)
}

func querySubjectSet(keys []*pb.TimeSeriesKey) map[string]bool {
	return factvalue.StringSet(querySubjects(keys))
}

func querySubjects(keys []*pb.TimeSeriesKey) []string {
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		subjectID := strings.TrimSpace(key.GetSubjectId())
		if subjectID == "" || seen[subjectID] {
			continue
		}
		seen[subjectID] = true
		out = append(out, subjectID)
	}
	return out
}

func timeSeriesRowMatchesQuery(row *pb.TimeSeriesRow, req *pb.QueryTimeSeriesRowsReq, allowSubjects map[string]bool) bool {
	if len(allowSubjects) > 0 && !allowSubjects[row.GetKey().GetSubjectId()] {
		return false
	}
	if !factvalue.TimeInRange(row.GetKey().GetDataTime(), req.GetTimeRange()) {
		return false
	}
	keys := req.GetKeys()
	if len(keys) == 0 {
		return true
	}
	for _, key := range keys {
		if timeSeriesRowMatchesKey(row, key) {
			return true
		}
	}
	return false
}

func timeSeriesRowMatchesKey(row *pb.TimeSeriesRow, key *pb.TimeSeriesKey) bool {
	if key == nil {
		return true
	}
	rowKey := row.GetKey()
	if key.GetSpaceId() != "" && key.GetSpaceId() != rowKey.GetSpaceId() {
		return false
	}
	if key.GetDatasetId() != "" && key.GetDatasetId() != rowKey.GetDatasetId() {
		return false
	}
	if key.GetSubjectId() != "" && key.GetSubjectId() != rowKey.GetSubjectId() {
		return false
	}
	if key.GetFreq() != "" && key.GetFreq() != rowKey.GetFreq() {
		return false
	}
	if !dimensionsEqual(key.GetDimensions(), rowKey.GetDimensions()) {
		return false
	}
	if key.GetDataTime() != "" {
		return factvalue.TimeInRange(rowKey.GetDataTime(), &pb.TimeRange{
			StartTime: key.GetDataTime(),
			EndTime:   key.GetDataTime(),
		})
	}
	return true
}

func dimensionsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
}

func filterRows(rows []*pb.TimeSeriesRow, filters []*pb.FilterExpr) ([]*pb.TimeSeriesRow, error) {
	if len(filters) == 0 {
		return rows, nil
	}
	out := rows[:0]
	for _, row := range rows {
		ok, err := rowMatchesFilters(row, filters)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func rowMatchesFilters(row *pb.TimeSeriesRow, filters []*pb.FilterExpr) (bool, error) {
	for _, filter := range filters {
		ok, err := rowMatchesFilter(row, filter)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func rowMatchesFilter(row *pb.TimeSeriesRow, filter *pb.FilterExpr) (bool, error) {
	if filter == nil || strings.TrimSpace(filter.GetExpr()) == "" {
		return true, nil
	}
	left, op, right, ok := parseSimpleFilter(filter.GetExpr())
	if !ok {
		return false, fmt.Errorf("unsupported filter expression %q", filter.GetExpr())
	}
	rowValue, ok := rowColumnValue(row, left)
	if !ok {
		return false, nil
	}
	expected := filterValue(right, filter.GetArgs())
	if expected == nil {
		return false, fmt.Errorf("unsupported filter value %q", right)
	}
	return compareTypedValues(rowValue, expected, op), nil
}

func parseSimpleFilter(expr string) (left, op, right string, ok bool) {
	expr = strings.TrimSpace(expr)
	for _, candidate := range []string{" contains ", "==", "!=", ">=", "<=", "=", ">", "<"} {
		if idx := strings.Index(expr, candidate); idx >= 0 {
			left = strings.TrimSpace(expr[:idx])
			right = strings.TrimSpace(expr[idx+len(candidate):])
			op = strings.TrimSpace(candidate)
			if left == "" || right == "" {
				return "", "", "", false
			}
			return left, op, right, true
		}
	}
	return "", "", "", false
}

func filterValue(token string, args map[string]*pb.TypedValue) *pb.TypedValue {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, "$") {
		return args[strings.TrimPrefix(token, "$")]
	}
	if strings.HasPrefix(token, "'") && strings.HasSuffix(token, "'") && len(token) >= 2 {
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: strings.Trim(token, "'")}}
	}
	if strings.HasPrefix(token, `"`) && strings.HasSuffix(token, `"`) && len(token) >= 2 {
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: strings.Trim(token, `"`)}}
	}
	if value, err := strconv.ParseFloat(token, 64); err == nil {
		return &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}
	}
	return nil
}

func sortRows(rows []*pb.TimeSeriesRow, sorts []*pb.SortSpec) {
	if len(sorts) == 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, spec := range sorts {
			left, _ := rowColumnValue(rows[i], spec.GetFieldName())
			right, _ := rowColumnValue(rows[j], spec.GetFieldName())
			cmp := factvalue.Compare(left, right)
			if cmp == 0 {
				continue
			}
			if spec.GetDesc() {
				return cmp > 0
			}
			return cmp < 0
		}
		if rows[i].GetKey().GetSubjectId() == rows[j].GetKey().GetSubjectId() {
			return rows[i].GetKey().GetDataTime() < rows[j].GetKey().GetDataTime()
		}
		return rows[i].GetKey().GetSubjectId() < rows[j].GetKey().GetSubjectId()
	})
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

func rowColumnValue(row *pb.TimeSeriesRow, name string) (*pb.TypedValue, bool) {
	for _, value := range row.GetColumns() {
		if value.GetColumnName() == name {
			return value.GetValue(), true
		}
	}
	return nil, false
}

func compareTypedValues(left, right *pb.TypedValue, op string) bool {
	if op == "contains" {
		return strings.Contains(factvalue.String(left), factvalue.String(right))
	}
	cmp := factvalue.Compare(left, right)
	switch op {
	case "=", "==":
		return cmp == 0
	case "!=":
		return cmp != 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	default:
		return false
	}
}

func compareForSort(left, right *pb.TypedValue) int {
	return factvalue.Compare(left, right)
}

func numericValue(value *pb.TypedValue) (float64, bool) {
	return factvalue.Numeric(value)
}

func typedValueString(value *pb.TypedValue) string {
	return factvalue.String(value)
}

func pageRows(rows []*pb.TimeSeriesRow, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult) {
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
	start := int((pageNo - 1) * size)
	if start > len(rows) {
		start = len(rows)
	}
	end := start + int(size)
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint64(len(rows)), HasMore: end < len(rows)}
}
