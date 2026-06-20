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
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
)

type Options struct {
	Path string
}

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
	if err := store.init(context.Background()); err != nil {
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

func (s *ViewStore) InsertRows(ctx context.Context, tableName string, rows []*pb.QueryViewRow) error {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`INSERT INTO %s (subject_id, data_time, row_json) VALUES (?, ?, ?)`, quoted))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(row)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := stmt.ExecContext(ctx, row.GetSubjectId(), row.GetDataTime(), string(raw)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *ViewStore) ListResultTables(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_name LIKE 'view_result_%'
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

func (s *ViewStore) QueryView(ctx context.Context, tableName string, req *pb.QueryViewReq) ([]*pb.QueryViewColumn, []*pb.QueryViewRow, *pb.PageResult, error) {
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
	allowSubjects := factvalue.StringSet(req.GetSubjectIds())
	var out []*pb.QueryViewRow
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, nil, nil, err
		}
		row := &pb.QueryViewRow{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), row); err != nil {
			return nil, nil, nil, err
		}
		if len(allowSubjects) > 0 && !allowSubjects[row.GetSubjectId()] {
			continue
		}
		if !factvalue.TimeInRange(row.GetDataTime(), req.GetQueryTime().GetTimeRange()) {
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
func buildPushdownPredicates(req *pb.QueryViewReq) (string, []any) {
	var clauses []string
	var args []any
	if subjects := req.GetSubjectIds(); len(subjects) > 0 {
		placeholders := make([]string, 0, len(subjects))
		for _, subject := range subjects {
			placeholders = append(placeholders, "?")
			args = append(args, subject)
		}
		clauses = append(clauses, "subject_id IN ("+strings.Join(placeholders, ",")+")")
	}
	return strings.Join(clauses, " AND "), args
}

func (s *ViewStore) loadColumns(ctx context.Context, tableName string) ([]*pb.QueryViewColumn, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT columns_json FROM moox_view_columns WHERE table_name = ?`, tableName).Scan(&raw); err != nil {
		return nil, err
	}
	rsp := &pb.QueryViewRsp{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), rsp); err != nil {
		return nil, err
	}
	return rsp.GetColumns(), nil
}

func encodeColumns(columns []*pb.ViewColumn) (string, error) {
	out := make([]*pb.QueryViewColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, &pb.QueryViewColumn{
			ColumnName: column.GetColumnName(),
			OriginType: column.GetOriginType(),
			OriginId:   column.GetOriginId(),
			ValueType:  column.GetValueType(),
		})
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&pb.QueryViewRsp{Columns: out})
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

func filterRows(rows []*pb.QueryViewRow, filters []*pb.FilterExpr) ([]*pb.QueryViewRow, error) {
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

func rowMatchesFilters(row *pb.QueryViewRow, filters []*pb.FilterExpr) (bool, error) {
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

func rowMatchesFilter(row *pb.QueryViewRow, filter *pb.FilterExpr) (bool, error) {
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

func sortRows(rows []*pb.QueryViewRow, sorts []*pb.SortSpec) {
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
		if rows[i].GetSubjectId() == rows[j].GetSubjectId() {
			return rows[i].GetDataTime() < rows[j].GetDataTime()
		}
		return rows[i].GetSubjectId() < rows[j].GetSubjectId()
	})
}

func projectColumns(columns []*pb.QueryViewColumn, includes []string) []*pb.QueryViewColumn {
	if len(includes) == 0 {
		return columns
	}
	allow := stringSet(includes)
	out := make([]*pb.QueryViewColumn, 0, len(includes))
	for _, column := range columns {
		if allow[column.GetColumnName()] {
			out = append(out, column)
		}
	}
	return out
}

func projectRows(rows []*pb.QueryViewRow, includes []string) []*pb.QueryViewRow {
	if len(includes) == 0 {
		return rows
	}
	allow := stringSet(includes)
	out := make([]*pb.QueryViewRow, 0, len(rows))
	for _, row := range rows {
		projected := &pb.QueryViewRow{
			SubjectId: row.GetSubjectId(),
			DataTime:  row.GetDataTime(),
			Values:    make([]*pb.ColumnValue, 0, len(includes)),
		}
		for _, value := range row.GetValues() {
			if allow[value.GetColumnName()] {
				projected.Values = append(projected.Values, value)
			}
		}
		out = append(out, projected)
	}
	return out
}

func rowColumnValue(row *pb.QueryViewRow, name string) (*pb.TypedValue, bool) {
	for _, value := range row.GetValues() {
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

func pageRows(rows []*pb.QueryViewRow, page *pb.Page) ([]*pb.QueryViewRow, *pb.PageResult) {
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
