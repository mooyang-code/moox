//go:build !cgo

package duckdb

import (
	"context"
	"errors"
	"strings"
	"sync"

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

func (s *ViewStore) QueryView(ctx context.Context, tableName string, req *pb.QueryViewReq) ([]*pb.QueryViewColumn, []*pb.QueryViewRow, *pb.PageResult, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	columns, ok := s.columns[tableName]
	if !ok {
		return nil, nil, nil, errors.New("duckdb view result table not found")
	}
	allowSubjects := stringSet(req.GetSubjectIds())
	var matched []*pb.QueryViewRow
	for _, row := range s.rows[tableName] {
		if len(allowSubjects) > 0 && !allowSubjects[row.GetSubjectId()] {
			continue
		}
		if !timeInRange(row.GetDataTime(), req.GetQueryTime().GetTimeRange()) {
			continue
		}
		matched = append(matched, row)
	}
	paged, page := pageRows(matched, req.GetPage())
	return columns, paged, page, nil
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
	if start := strings.TrimSpace(timeRange.GetStartTime()); start != "" && value < start {
		return false
	}
	if end := strings.TrimSpace(timeRange.GetEndTime()); end != "" && value > end {
		return false
	}
	return true
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
