//go:build !cgo

package duckdb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type Options struct {
	Path string
}

type ViewStore struct {
	mu      sync.Mutex
	columns map[string][]*pb.QueryViewColumn
	rows    map[string][]*pb.QueryViewRow
}

func Open(opts Options) (*ViewStore, error) {
	if opts.Path == "" {
		return nil, errors.New("duckdb path is required")
	}
	return &ViewStore{
		columns: make(map[string][]*pb.QueryViewColumn),
		rows:    make(map[string][]*pb.QueryViewRow),
	}, nil
}

func (s *ViewStore) Close() error {
	return nil
}

func (s *ViewStore) CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	s.columns[tableName] = convertColumns(columns)
	if s.rows[tableName] == nil {
		s.rows[tableName] = nil
	}
	return nil
}

func (s *ViewStore) InsertRows(ctx context.Context, tableName string, rows []*pb.QueryViewRow) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[tableName] = append(s.rows[tableName], rows...)
	return nil
}

func (s *ViewStore) ListResultTables(ctx context.Context) ([]string, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.rows))
	for tableName := range s.rows {
		if strings.HasPrefix(tableName, "view_result_") {
			out = append(out, tableName)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *ViewStore) DropResultTable(ctx context.Context, tableName string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, tableName)
	delete(s.columns, tableName)
	return nil
}

func (s *ViewStore) QueryView(ctx context.Context, tableName string, req *pb.QueryViewReq) ([]*pb.QueryViewColumn, []*pb.QueryViewRow, *pb.PageResult, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	columns, ok := s.columns[tableName]
	if !ok {
		return nil, nil, nil, errors.New("duckdb view result table not found")
	}
	allowSubjects := factvalue.StringSet(req.GetSubjectIds())
	var matched []*pb.QueryViewRow
	for _, row := range s.rows[tableName] {
		if len(allowSubjects) > 0 && !allowSubjects[row.GetSubjectId()] {
			continue
		}
		if !factvalue.TimeInRange(row.GetDataTime(), req.GetQueryTime().GetTimeRange()) {
			continue
		}
		matched = append(matched, row)
	}
	matched, err := filterRows(matched, req.GetFilters())
	if err != nil {
		return nil, nil, nil, err
	}
	sortRows(matched, req.GetSorts())
	projectedColumns := projectColumns(columns, req.GetColumnNames())
	projectedRows := projectRows(matched, req.GetColumnNames())
	paged, page := pageRows(projectedRows, req.GetPage())
	return projectedColumns, paged, page, nil
}

func convertColumns(columns []*pb.ViewColumn) []*pb.QueryViewColumn {
	out := make([]*pb.QueryViewColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, &pb.QueryViewColumn{
			ColumnName: column.GetColumnName(),
			OriginType: column.GetOriginType(),
			OriginId:   column.GetOriginId(),
			ValueType:  column.GetValueType(),
		})
	}
	return out
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
	allow := factvalue.StringSet(includes)
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
	allow := factvalue.StringSet(includes)
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
