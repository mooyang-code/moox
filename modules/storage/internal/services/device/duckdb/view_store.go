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
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
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

func (s *ViewStore) QueryView(ctx context.Context, tableName string, req *pb.QueryViewReq) ([]*pb.QueryViewColumn, []*pb.QueryViewRow, *pb.PageResult, error) {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return nil, nil, nil, err
	}
	columns, err := s.loadColumns(ctx, tableName)
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT row_json FROM %s ORDER BY subject_id, data_time`, quoted))
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()

	allowSubjects := stringSet(req.GetSubjectIds())
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
		if !timeInRange(row.GetDataTime(), req.GetQueryTime().GetTimeRange()) {
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
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func timeInRange(value string, timeRange *pb.TimeRange) bool {
	if timeRange == nil {
		return true
	}
	valueTime, valueOK := parseTimeValue(value)
	if start := strings.TrimSpace(timeRange.GetStartTime()); start != "" {
		if startTime, startOK := parseTimeValue(start); valueOK && startOK {
			if timeRange.GetStartInclusive() {
				if valueTime.Before(startTime) {
					return false
				}
			} else if !valueTime.After(startTime) {
				return false
			}
		} else if !textAfterLowerBound(value, start, timeRange.GetStartInclusive()) {
			return false
		}
	}
	if end := strings.TrimSpace(timeRange.GetEndTime()); end != "" {
		if endTime, endOK := parseTimeValue(end); valueOK && endOK {
			if timeRange.GetEndInclusive() {
				if valueTime.After(endTime) {
					return false
				}
			} else if !valueTime.Before(endTime) {
				return false
			}
		} else if !textBeforeUpperBound(value, end, timeRange.GetEndInclusive()) {
			return false
		}
	}
	return true
}

func parseTimeValue(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func textAfterLowerBound(value string, start string, inclusive bool) bool {
	cmp := strings.Compare(value, start)
	if inclusive {
		return cmp >= 0
	}
	return cmp > 0
}

func textBeforeUpperBound(value string, end string, inclusive bool) bool {
	cmp := strings.Compare(value, end)
	if inclusive {
		return cmp <= 0
	}
	return cmp < 0
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
			cmp := compareForSort(left, right)
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
		return strings.Contains(typedValueString(left), typedValueString(right))
	}
	cmp := compareForSort(left, right)
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
	leftNumber, leftOK := numericValue(left)
	rightNumber, rightOK := numericValue(right)
	if leftOK && rightOK {
		switch {
		case leftNumber < rightNumber:
			return -1
		case leftNumber > rightNumber:
			return 1
		default:
			return 0
		}
	}
	leftText := typedValueString(left)
	rightText := typedValueString(right)
	switch {
	case leftText < rightText:
		return -1
	case leftText > rightText:
		return 1
	default:
		return 0
	}
}

func numericValue(value *pb.TypedValue) (float64, bool) {
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_IntValue:
		return float64(v.IntValue), true
	case *pb.TypedValue_DoubleValue:
		return v.DoubleValue, true
	default:
		return 0, false
	}
}

func typedValueString(value *pb.TypedValue) string {
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		return v.StringValue
	case *pb.TypedValue_IntValue:
		return strconv.FormatInt(v.IntValue, 10)
	case *pb.TypedValue_DoubleValue:
		return strconv.FormatFloat(v.DoubleValue, 'g', -1, 64)
	case *pb.TypedValue_BoolValue:
		return strconv.FormatBool(v.BoolValue)
	case *pb.TypedValue_TimeValue:
		return v.TimeValue
	case *pb.TypedValue_JsonValue:
		return v.JsonValue
	case *pb.TypedValue_BytesValue:
		return string(v.BytesValue)
	default:
		return ""
	}
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
