package pebble

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type Options struct {
	Path string
}

type Store struct {
	db *cpebble.DB
}

func Open(opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("pebble path is required")
	}
	db, err := cpebble.Open(opts.Path, &cpebble.Options{})
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) WriteRows(ctx context.Context, rows []*pb.DataRow, mode pb.WriteMode) error {
	_ = ctx
	if len(rows) == 0 {
		return nil
	}
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}
	if mode == pb.WriteMode_WRITE_MODE_OVERWRITE {
		for _, row := range rows {
			if err := s.deleteScope(row.GetKey().GetScope()); err != nil {
				return err
			}
		}
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, row := range rows {
		if err := validateRow(row); err != nil {
			return err
		}
		data, err := proto.Marshal(row)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(encodeRowKey(row)), data, cpebble.Sync); err != nil {
			return err
		}
	}
	return batch.Commit(cpebble.Sync)
}

func (s *Store) ReadRows(ctx context.Context, scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, rowIDs []string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	_ = ctx
	if err := validateScope(scope); err != nil {
		return nil, nil, err
	}
	if mode == pb.ReadMode_READ_MODE_UNSPECIFIED {
		mode = pb.ReadMode_READ_MODE_RANGE
	}
	if mode == pb.ReadMode_READ_MODE_POINT && len(rowIDs) == 0 {
		return nil, nil, errors.New("row_ids is required for point read")
	}
	prefix := []byte(encodeScopePrefix(scope))
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: nextPrefix(prefix)})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()

	allowRows := makeSet(rowIDs)
	var rows []*pb.DataRow
	for valid := iter.First(); valid; valid = iter.Next() {
		row := &pb.DataRow{}
		if err := proto.Unmarshal(iter.Value(), row); err != nil {
			return nil, nil, err
		}
		if !scopeMatches(row.GetKey().GetScope(), scope) {
			continue
		}
		if mode == pb.ReadMode_READ_MODE_POINT && !allowRows[row.GetKey().GetRowId()] {
			continue
		}
		if mode != pb.ReadMode_READ_MODE_POINT && !timeInRange(row.GetKey().GetDataTime(), timeRange) {
			continue
		}
		if mode == pb.ReadMode_READ_MODE_LATEST_BEFORE && snapshotTime != "" && row.GetKey().GetDataTime() > snapshotTime {
			continue
		}
		rows = append(rows, filterRowColumns(row, columnNames))
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	if mode == pb.ReadMode_READ_MODE_LATEST_BEFORE {
		rows = latestRows(rows)
	}
	sortRows(rows)
	paged, result := pageRows(rows, page)
	return paged, result, nil
}

func (s *Store) deleteScope(scope *pb.DataScope) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	prefix := []byte(encodeScopePrefix(scope))
	return s.db.DeleteRange(prefix, nextPrefix(prefix), cpebble.Sync)
}

func validateRow(row *pb.DataRow) error {
	if row == nil {
		return errors.New("row is required")
	}
	if row.GetKey() == nil {
		return errors.New("key is required")
	}
	return validateScope(row.GetKey().GetScope())
}

func validateScope(scope *pb.DataScope) error {
	if scope == nil {
		return errors.New("scope is required")
	}
	if scope.GetSpaceId() == "" {
		return errors.New("space_id is required")
	}
	if scope.GetDatasetId() == "" {
		return errors.New("dataset_id is required")
	}
	return nil
}

func scopeMatches(rowScope *pb.DataScope, query *pb.DataScope) bool {
	if rowScope.GetSpaceId() != query.GetSpaceId() || rowScope.GetDatasetId() != query.GetDatasetId() {
		return false
	}
	if query.GetSubjectId() != "" && rowScope.GetSubjectId() != query.GetSubjectId() {
		return false
	}
	if query.GetFreq() != "" && rowScope.GetFreq() != query.GetFreq() {
		return false
	}
	for key, value := range query.GetDimensions() {
		if rowScope.GetDimensions()[key] != value {
			return false
		}
	}
	return true
}

func nextPrefix(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}
	out := bytes.Clone(prefix)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] < 0xff {
			out[i]++
			return out[:i+1]
		}
	}
	return nil
}

func filterRowColumns(row *pb.DataRow, includes []string) *pb.DataRow {
	if len(includes) == 0 {
		return row
	}
	allow := makeSet(includes)
	filtered := proto.Clone(row).(*pb.DataRow)
	filtered.Columns = filtered.Columns[:0]
	for _, column := range row.GetColumns() {
		if allow[column.GetColumnName()] {
			filtered.Columns = append(filtered.Columns, column)
		}
	}
	return filtered
}

func latestRows(rows []*pb.DataRow) []*pb.DataRow {
	latest := make(map[string]*pb.DataRow)
	for _, row := range rows {
		key := row.GetKey().GetScope().GetSubjectId()
		if key == "" {
			key = row.GetKey().GetScope().GetDatasetId()
		}
		if prev := latest[key]; prev == nil || row.GetKey().GetDataTime() > prev.GetKey().GetDataTime() {
			latest[key] = row
		}
	}
	out := make([]*pb.DataRow, 0, len(latest))
	for _, row := range latest {
		out = append(out, row)
	}
	return out
}

func sortRows(rows []*pb.DataRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.GetKey().GetScope().GetSubjectId() != right.GetKey().GetScope().GetSubjectId() {
			return left.GetKey().GetScope().GetSubjectId() < right.GetKey().GetScope().GetSubjectId()
		}
		if left.GetKey().GetDataTime() != right.GetKey().GetDataTime() {
			return left.GetKey().GetDataTime() < right.GetKey().GetDataTime()
		}
		return left.GetKey().GetRowId() < right.GetKey().GetRowId()
	})
}

func timeInRange(value string, timeRange *pb.TimeRange) bool {
	if timeRange == nil {
		return true
	}
	if start := strings.TrimSpace(timeRange.GetStartTime()); start != "" {
		if value < start {
			return false
		}
	}
	if end := strings.TrimSpace(timeRange.GetEndTime()); end != "" {
		if value > end {
			return false
		}
	}
	return true
}

func pageRows(rows []*pb.DataRow, page *pb.Page) ([]*pb.DataRow, *pb.PageResult) {
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

func makeSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func debugRowKey(row *pb.DataRow, fallback int) string {
	if row.GetKey().GetRowId() != "" {
		return "id:" + row.GetKey().GetRowId()
	}
	if row.GetKey().GetDataTime() != "" {
		return "time:" + row.GetKey().GetDataTime()
	}
	return fmt.Sprintf("append:%d", fallback)
}
