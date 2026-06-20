package pebble

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type Options struct {
	Path              string
	DisableSyncWrites bool
}

type Store struct {
	db           *cpebble.DB
	writeOptions *cpebble.WriteOptions
	lockMu       sync.Mutex
	locks        map[string]*rowLock
}

type rowLock struct {
	mu   sync.Mutex
	refs int
}

func Open(opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("pebble path is required")
	}
	db, err := cpebble.Open(opts.Path, &cpebble.Options{})
	if err != nil {
		return nil, err
	}
	writeOptions := cpebble.Sync
	if opts.DisableSyncWrites {
		writeOptions = cpebble.NoSync
	}
	return &Store{db: db, writeOptions: writeOptions, locks: make(map[string]*rowLock)}, nil
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
	// 先全部校验，避免在批次中途失败后留下半成品写入。
	for _, row := range rows {
		if err := validateRow(row); err != nil {
			return err
		}
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	pending := make(map[string]*pb.DataRow, len(rows))
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		key := encodeRowKey(row)
		keys = append(keys, key)
	}
	unlock := s.lockRows(keys)
	defer unlock()
	for _, row := range rows {
		key := encodeRowKey(row)
		base := pending[key]
		if base == nil {
			existing, err := s.getRow(key)
			if err != nil {
				return err
			}
			base = existing
		}
		pending[key] = mergeRow(base, row)
	}
	for key, row := range pending {
		data, err := proto.Marshal(row)
		if err != nil {
			return err
		}
		if err := batch.Set([]byte(key), data, s.writeOptions); err != nil {
			return err
		}
	}
	return batch.Commit(s.writeOptions)
}

func (s *Store) lockRows(keys []string) func() {
	keys = uniqueSorted(keys)
	locks := make([]*rowLock, 0, len(keys))
	s.lockMu.Lock()
	for _, key := range keys {
		lock := s.locks[key]
		if lock == nil {
			lock = &rowLock{}
			s.locks[key] = lock
		}
		lock.refs++
		locks = append(locks, lock)
	}
	s.lockMu.Unlock()
	for _, lock := range locks {
		lock.mu.Lock()
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].mu.Unlock()
		}
		s.lockMu.Lock()
		defer s.lockMu.Unlock()
		for _, key := range keys {
			lock := s.locks[key]
			if lock == nil {
				continue
			}
			lock.refs--
			if lock.refs == 0 {
				delete(s.locks, key)
			}
		}
	}
}

func (s *Store) getRow(key string) (*pb.DataRow, error) {
	data, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	row := &pb.DataRow{}
	if err := proto.Unmarshal(data, row); err != nil {
		return nil, err
	}
	return row, nil
}

func mergeRow(base *pb.DataRow, patch *pb.DataRow) *pb.DataRow {
	if base == nil {
		return proto.Clone(patch).(*pb.DataRow)
	}
	merged := proto.Clone(base).(*pb.DataRow)
	merged.Key = proto.Clone(patch.GetKey()).(*pb.DataKey)
	positions := make(map[string]int, len(merged.GetColumns()))
	for idx, column := range merged.GetColumns() {
		positions[column.GetColumnName()] = idx
	}
	for _, column := range patch.GetColumns() {
		copied := proto.Clone(column).(*pb.ColumnValue)
		if idx, ok := positions[column.GetColumnName()]; ok {
			merged.Columns[idx] = copied
			continue
		}
		positions[column.GetColumnName()] = len(merged.Columns)
		merged.Columns = append(merged.Columns, copied)
	}
	if len(patch.GetAttributes()) > 0 {
		if merged.Attributes == nil {
			merged.Attributes = make(map[string]string, len(patch.GetAttributes()))
		}
		for key, value := range patch.GetAttributes() {
			merged.Attributes[key] = value
		}
	}
	return merged
}

func (s *Store) ReadRows(ctx context.Context, scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, objectID string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	_ = ctx
	if err := validateScope(scope); err != nil {
		return nil, nil, err
	}
	if mode == pb.ReadMode_READ_MODE_UNSPECIFIED {
		mode = pb.ReadMode_READ_MODE_RANGE
	}
	if objectID != "" {
		return s.readObjectRows(scope, mode, timeRange, snapshotTime, objectID, columnNames, page)
	}
	if scope.GetFreq() == "" {
		return s.readMixedRows(scope, mode, timeRange, snapshotTime, columnNames, page)
	}
	lower, upper := readBounds(scope, timeRange)
	cursorMode := false
	if cursorLower, ok := cursorLowerBound(scope, mode, snapshotTime, page, lower, upper); ok {
		lower = cursorLower
		cursorMode = true
	}
	return s.readRowsInBounds(scope, mode, timeRange, snapshotTime, "", columnNames, page, lower, upper, cursorMode)
}

func (s *Store) readMixedRows(scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	bounds := [][2][]byte{}
	objectID := ""
	if scope.GetSubjectId() != "" {
		objectID = scope.GetSubjectId()
	}
	objectLower := []byte(objectReadPrefix(scope, objectID))
	bounds = append(bounds, [2][]byte{objectLower, nextPrefix(objectLower)})
	timeLower, timeUpper := readBounds(scope, timeRange)
	bounds = append(bounds, [2][]byte{timeLower, timeUpper})
	return s.readRowsAcrossBounds(scope, mode, timeRange, snapshotTime, "", columnNames, page, bounds)
}

func (s *Store) readObjectRows(scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, objectID string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	bounds := [][2][]byte{}
	objectLower := []byte(objectReadPrefix(scope, objectID))
	bounds = append(bounds, [2][]byte{objectLower, nextPrefix(objectLower)})
	return s.readRowsAcrossBounds(scope, mode, timeRange, snapshotTime, objectID, columnNames, page, bounds)
}

func (s *Store) readRowsInBounds(scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, objectID string, columnNames []string, page *pb.Page, lower []byte, upper []byte, cursorMode bool) ([]*pb.DataRow, *pb.PageResult, error) {
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()

	if cursorMode {
		return readRowsByCursor(iter, scope, mode, timeRange, snapshotTime, objectID, columnNames, page)
	}
	var rows []*pb.DataRow
	for valid := iter.First(); valid; valid = iter.Next() {
		row := &pb.DataRow{}
		if err := proto.Unmarshal(iter.Value(), row); err != nil {
			return nil, nil, err
		}
		if !rowMatchesRead(row, scope, mode, timeRange, snapshotTime, objectID) {
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

func (s *Store) readRowsAcrossBounds(scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, objectID string, columnNames []string, page *pb.Page, bounds [][2][]byte) ([]*pb.DataRow, *pb.PageResult, error) {
	var rows []*pb.DataRow
	for _, bound := range bounds {
		iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: bound[0], UpperBound: bound[1]})
		if err != nil {
			return nil, nil, err
		}
		for valid := iter.First(); valid; valid = iter.Next() {
			row := &pb.DataRow{}
			if err := proto.Unmarshal(iter.Value(), row); err != nil {
				iter.Close()
				return nil, nil, err
			}
			if !rowMatchesRead(row, scope, mode, timeRange, snapshotTime, objectID) {
				continue
			}
			rows = append(rows, filterRowColumns(row, columnNames))
		}
		if err := iter.Error(); err != nil {
			iter.Close()
			return nil, nil, err
		}
		iter.Close()
	}
	if mode == pb.ReadMode_READ_MODE_LATEST_BEFORE {
		rows = latestRows(rows)
	}
	sortRows(rows)
	paged, result := pageRows(rows, page)
	return paged, result, nil
}

func cursorLowerBound(scope *pb.DataScope, mode pb.ReadMode, snapshotTime string, page *pb.Page, lower []byte, upper []byte) ([]byte, bool) {
	if page == nil || page.GetCursor() == "" {
		return nil, false
	}
	if mode != pb.ReadMode_READ_MODE_RANGE || snapshotTime != "" {
		return nil, false
	}
	if scope.GetSubjectId() == "" || scope.GetFreq() == "" || len(scope.GetDimensions()) == 0 {
		return nil, false
	}
	cursor := []byte(page.GetCursor())
	if bytes.Compare(cursor, lower) < 0 {
		return nil, false
	}
	if len(upper) > 0 && bytes.Compare(cursor, upper) >= 0 {
		return nil, false
	}
	next := nextPrefix(cursor)
	if len(next) == 0 {
		return nil, false
	}
	return next, true
}

func readRowsByCursor(iter *cpebble.Iterator, scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, objectID string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	size := pageSize(page)
	limit := int(size) + 1
	rows := make([]*pb.DataRow, 0, limit)
	for valid := iter.First(); valid; valid = iter.Next() {
		row := &pb.DataRow{}
		if err := proto.Unmarshal(iter.Value(), row); err != nil {
			return nil, nil, err
		}
		if !rowMatchesRead(row, scope, mode, timeRange, snapshotTime, objectID) {
			continue
		}
		rows = append(rows, filterRowColumns(row, columnNames))
		if len(rows) >= limit {
			break
		}
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	hasMore := len(rows) > int(size)
	if hasMore {
		rows = rows[:size]
	}
	next := ""
	if hasMore && len(rows) > 0 {
		next = encodeRowKey(rows[len(rows)-1])
	}
	return rows, &pb.PageResult{
		Size:       size,
		HasMore:    hasMore,
		NextCursor: next,
	}, nil
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

func scopeMatches(row *pb.DataRow, query *pb.DataScope) bool {
	rowScope := row.GetKey().GetScope()
	if rowScope.GetSpaceId() != query.GetSpaceId() || rowScope.GetDatasetId() != query.GetDatasetId() {
		return false
	}
	if query.GetSubjectId() != "" {
		rowSubjectID := rowScope.GetSubjectId()
		if rowSubjectID == "" && rowScope.GetFreq() == "" {
			rowSubjectID = row.GetKey().GetRowId()
		}
		if rowSubjectID != query.GetSubjectId() {
			return false
		}
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

func rowMatchesRead(row *pb.DataRow, scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, objectID string) bool {
	if !scopeMatches(row, scope) {
		return false
	}
	if objectID != "" && row.GetKey().GetRowId() != objectID {
		return false
	}
	if !factvalue.TimeInRangeClosed(row.GetKey().GetDataTime(), timeRange) {
		return false
	}
	if mode == pb.ReadMode_READ_MODE_LATEST_BEFORE && snapshotTime != "" && row.GetKey().GetDataTime() > snapshotTime {
		return false
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

func pageRows(rows []*pb.DataRow, page *pb.Page) ([]*pb.DataRow, *pb.PageResult) {
	pageNo := uint32(1)
	size := pageSize(page)
	cursor := ""
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		cursor = page.GetCursor()
	}
	// 游标分页：利用结果按 row key 有序的特性，从 cursor 之后开始返回 size 条，
	// 避免深翻页时重复扫描前面的数据（keyset pagination）。
	if cursor != "" {
		start := 0
		for idx, row := range rows {
			if encodeRowKey(row) > cursor {
				start = idx
				break
			}
			start = idx + 1
		}
		end := start + int(size)
		if end > len(rows) {
			end = len(rows)
		}
		next := ""
		if end < len(rows) && end > start {
			next = encodeRowKey(rows[end-1])
		}
		return rows[start:end], &pb.PageResult{
			Size:       size,
			Total:      uint64(len(rows)),
			HasMore:    end < len(rows),
			NextCursor: next,
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
	next := ""
	if end < len(rows) && end > start {
		next = encodeRowKey(rows[end-1])
	}
	return rows[start:end], &pb.PageResult{
		Page:       pageNo,
		Size:       size,
		Total:      uint64(len(rows)),
		HasMore:    end < len(rows),
		NextCursor: next,
	}
}

func pageSize(page *pb.Page) uint32 {
	if page != nil && page.GetSize() > 0 {
		return page.GetSize()
	}
	return 1000
}

func makeSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
