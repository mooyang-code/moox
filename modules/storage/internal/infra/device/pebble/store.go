package pebble

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

// Options 保存 Pebble 主存打开配置。
type Options struct {
	Path              string
	DisableSyncWrites bool
}

// Store 封装 Pebble 主存的行级读写能力。
type Store struct {
	db           *cpebble.DB
	writeOptions *cpebble.WriteOptions
	lockMu       sync.Mutex
	locks        map[string]*rowLock
}

// rowLock 保存同一行合并写入时使用的互斥锁。
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

func (s *Store) WriteRows(ctx context.Context, rows []*pb.PrimaryStoreRow) error {
	_ = ctx
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		if err := validateRow(row); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, encodeRowKey(row))
	}
	unlock := s.lockRows(keys)
	defer unlock()

	pending := make(map[string]*pb.PrimaryStoreRow, len(rows))
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

	batch := s.db.NewBatch()
	defer batch.Close()
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

func (s *Store) getRow(key string) (*pb.PrimaryStoreRow, error) {
	data, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	row := &pb.PrimaryStoreRow{}
	if err := proto.Unmarshal(data, row); err != nil {
		return nil, err
	}
	return row, nil
}

func mergeRow(base *pb.PrimaryStoreRow, patch *pb.PrimaryStoreRow) *pb.PrimaryStoreRow {
	if base == nil {
		return proto.Clone(patch).(*pb.PrimaryStoreRow)
	}
	merged := proto.Clone(base).(*pb.PrimaryStoreRow)
	merged.Key = proto.Clone(patch.GetKey()).(*pb.PrimaryStoreKey)
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

func isNullColumn(column *pb.ColumnValue) bool {
	return column == nil || column.GetValue() == nil
}

func (s *Store) ReadRows(ctx context.Context, keys []*pb.PrimaryStoreKey, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	_ = ctx
	if len(keys) == 0 {
		return nil, &pb.PageResult{Size: pageSize(page)}, nil
	}
	for _, key := range keys {
		if err := validateKey(key); err != nil {
			return nil, nil, err
		}
	}
	if canReadExact(keys, versionRange, page) {
		return s.readExactRows(keys, order, columnNames, page)
	}
	var rows []*pb.PrimaryStoreRow
	for _, key := range keys {
		readRows, err := s.readRowsForKey(key, versionRange, order, columnNames, page)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, readRows...)
	}
	sortRows(rows, order)
	paged, result := pageRows(rows, page, order)
	return paged, result, nil
}

func (s *Store) ScanRows(ctx context.Context, target *pb.PrimaryStoreTarget, dataKind pb.DataKind, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	_ = ctx
	if target == nil {
		return nil, nil, errors.New("target is required")
	}
	if target.GetSpaceId() == "" {
		return nil, nil, errors.New("space_id is required")
	}
	if target.GetDatasetId() == "" {
		return nil, nil, errors.New("dataset_id is required")
	}
	if kindPrefix(dataKind) == "" {
		return nil, nil, errors.New("data_kind is required")
	}
	prefix := []byte(encodeDatasetPrefix(dataKind, target.GetSpaceId(), target.GetDatasetId()))
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: nextPrefix(prefix)})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()

	rows := make([]*pb.PrimaryStoreRow, 0)
	for valid := iter.First(); valid; valid = iter.Next() {
		row := &pb.PrimaryStoreRow{}
		if err := proto.Unmarshal(iter.Value(), row); err != nil {
			return nil, nil, err
		}
		if !versionRangeContains(row.GetKey().GetVersion(), versionRange) {
			continue
		}
		rows = append(rows, filterRowColumns(row, columnNames))
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	sortRows(rows, order)
	paged, result := pageRows(rows, page, order)
	return paged, result, nil
}

func canReadExact(keys []*pb.PrimaryStoreKey, versionRange *pb.VersionRange, page *pb.Page) bool {
	if page != nil && page.GetCursor() != "" {
		return false
	}
	if versionRange != nil {
		return false
	}
	for _, key := range keys {
		if key.GetVersion() == "" {
			return false
		}
	}
	return true
}

func (s *Store) readExactRows(keys []*pb.PrimaryStoreKey, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	rows := make([]*pb.PrimaryStoreRow, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		encoded := encodePrimaryStoreKey(key)
		if seen[encoded] {
			continue
		}
		seen[encoded] = true
		row, err := s.getRow(encoded)
		if err != nil {
			return nil, nil, err
		}
		if row != nil {
			rows = append(rows, filterRowColumns(row, columnNames))
		}
	}
	sortRows(rows, order)
	paged, result := pageRows(rows, page, order)
	return paged, result, nil
}

func (s *Store) readRowsForKey(key *pb.PrimaryStoreKey, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, error) {
	if key.GetVersion() != "" && versionRange == nil {
		row, err := s.getRow(encodePrimaryStoreKey(key))
		if err != nil || row == nil {
			return nil, err
		}
		return []*pb.PrimaryStoreRow{filterRowColumns(row, columnNames)}, nil
	}
	lower, upper := keyBounds(key, versionRange)
	if cursor := page.GetCursor(); cursor != "" && order != pb.SortOrder_SORT_ORDER_DESC {
		cursorBytes := []byte(cursor)
		if bytes.Compare(cursorBytes, lower) >= 0 && (len(upper) == 0 || bytes.Compare(cursorBytes, upper) < 0) {
			if next := nextPrefix(cursorBytes); len(next) > 0 {
				lower = next
			}
		}
	}
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var rows []*pb.PrimaryStoreRow
	for valid := iter.First(); valid; valid = iter.Next() {
		row := &pb.PrimaryStoreRow{}
		if err := proto.Unmarshal(iter.Value(), row); err != nil {
			return nil, err
		}
		rows = append(rows, filterRowColumns(row, columnNames))
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return rows, nil
}

func validateRow(row *pb.PrimaryStoreRow) error {
	if row == nil {
		return errors.New("row is required")
	}
	return validateKey(row.GetKey())
}

func validateKey(key *pb.PrimaryStoreKey) error {
	if key == nil {
		return errors.New("key is required")
	}
	if key.GetSpaceId() == "" {
		return errors.New("space_id is required")
	}
	if key.GetDatasetId() == "" {
		return errors.New("dataset_id is required")
	}
	if kindPrefix(key.GetDataKind()) == "" {
		return errors.New("data_kind is required")
	}
	if key.GetKey() == "" {
		return errors.New("key is required")
	}
	return nil
}

func versionRangeContains(version string, versionRange *pb.VersionRange) bool {
	if versionRange == nil {
		return true
	}
	normalized := normalizeVersionForKey(version)
	if start := versionRange.GetStartVersion(); start != "" && normalized < normalizeVersionForKey(start) {
		return false
	}
	if end := versionRange.GetEndVersion(); end != "" && normalized > normalizeVersionForKey(end) {
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

func filterRowColumns(row *pb.PrimaryStoreRow, includes []string) *pb.PrimaryStoreRow {
	if len(includes) == 0 {
		return row
	}
	allow := makeSet(includes)
	filtered := proto.Clone(row).(*pb.PrimaryStoreRow)
	filtered.Columns = filtered.Columns[:0]
	for _, column := range row.GetColumns() {
		if allow[column.GetColumnName()] {
			filtered.Columns = append(filtered.Columns, column)
		}
	}
	return filtered
}

func sortRows(rows []*pb.PrimaryStoreRow, order pb.SortOrder) {
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i].GetKey()
		right := rows[j].GetKey()
		if left.GetDataKind() != right.GetDataKind() {
			return left.GetDataKind() < right.GetDataKind()
		}
		if left.GetSpaceId() != right.GetSpaceId() {
			return left.GetSpaceId() < right.GetSpaceId()
		}
		if left.GetDatasetId() != right.GetDatasetId() {
			return left.GetDatasetId() < right.GetDatasetId()
		}
		if left.GetKey() != right.GetKey() {
			return left.GetKey() < right.GetKey()
		}
		if left.GetVersion() != right.GetVersion() {
			if order == pb.SortOrder_SORT_ORDER_DESC {
				return left.GetVersion() > right.GetVersion()
			}
			return left.GetVersion() < right.GetVersion()
		}
		return false
	})
}

func pageRows(rows []*pb.PrimaryStoreRow, page *pb.Page, order pb.SortOrder) ([]*pb.PrimaryStoreRow, *pb.PageResult) {
	pageNo := uint32(1)
	size := pageSize(page)
	cursor := ""
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		cursor = page.GetCursor()
	}
	if cursor != "" {
		start := cursorStart(rows, cursor, order)
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

func cursorStart(rows []*pb.PrimaryStoreRow, cursor string, order pb.SortOrder) int {
	for idx, row := range rows {
		if encodeRowKey(row) == cursor {
			return idx + 1
		}
	}
	for idx, row := range rows {
		encoded := encodeRowKey(row)
		if order == pb.SortOrder_SORT_ORDER_DESC {
			if encoded < cursor {
				return idx
			}
			continue
		}
		if encoded > cursor {
			return idx
		}
	}
	return len(rows)
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
