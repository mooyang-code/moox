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
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

// Options 保存无 DuckDB 构建环境下的占位配置。
type Options struct {
	Path string
}

// ViewStore 是无 DuckDB 构建环境下的占位实现。
type ViewStore struct {
	mu      sync.Mutex
	columns map[string][]*pb.ResultColumn
	rows    map[string][]*pb.TimeSeriesRow
}

func Open(opts Options) (*ViewStore, error) {
	if opts.Path == "" {
		return nil, errors.New("duckdb path is required")
	}
	return &ViewStore{
		columns: make(map[string][]*pb.ResultColumn),
		rows:    make(map[string][]*pb.TimeSeriesRow),
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

func (s *ViewStore) InsertRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	positions := make(map[string]int, len(s.rows[tableName]))
	for idx, row := range s.rows[tableName] {
		positions[timeSeriesRowKey(row)+"|"+row.GetKey().GetDataTime()] = idx
	}
	for _, row := range rows {
		key := timeSeriesRowKey(row) + "|" + row.GetKey().GetDataTime()
		if idx, ok := positions[key]; ok {
			s.rows[tableName][idx] = mergeTimeSeriesRow(s.rows[tableName][idx], row)
			continue
		}
		positions[key] = len(s.rows[tableName])
		s.rows[tableName] = append(s.rows[tableName], proto.Clone(row).(*pb.TimeSeriesRow))
	}
	return nil
}

func (s *ViewStore) ListResultTables(ctx context.Context) ([]string, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.rows))
	for tableName := range s.rows {
		if strings.HasPrefix(tableName, "ts_view_") || strings.HasPrefix(tableName, "view_result_") {
			out = append(out, tableName)
		}
	}
	sort.Strings(out)
	return out, nil
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

func (s *ViewStore) DropResultTable(ctx context.Context, tableName string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, tableName)
	delete(s.columns, tableName)
	return nil
}

func (s *ViewStore) QueryTimeSeriesRows(ctx context.Context, tableName string, req *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	columns, ok := s.columns[tableName]
	if !ok {
		return nil, nil, nil, errors.New("duckdb view result table not found")
	}
	allowSubjects := querySubjectSet(req.GetKeys())
	var matched []*pb.TimeSeriesRow
	for _, row := range s.rows[tableName] {
		if !timeSeriesRowMatchesQuery(row, req, allowSubjects) {
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

func convertColumns(columns []*pb.ViewColumn) []*pb.ResultColumn {
	out := make([]*pb.ResultColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, &pb.ResultColumn{
			ColumnName: column.GetColumnName(),
			OriginType: column.GetOriginType(),
			OriginId:   column.GetOriginId(),
			ValueType:  column.GetValueType(),
		})
	}
	return out
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
			cmp := compareForSort(left, right)
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
	allow := factvalue.StringSet(includes)
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
	allow := factvalue.StringSet(includes)
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
