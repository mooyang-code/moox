package pebble

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	cpebble "github.com/cockroachdb/pebble"
	contracts "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
)

// Options 保存 Pebble 主存打开配置。
type Options struct {
	Path              string
	DisableSyncWrites bool
	ShardID           string
}

// Store 封装 Pebble 主存的行级读写能力。
type Store struct {
	db           *cpebble.DB
	writeOptions *cpebble.WriteOptions
	lockMu       sync.Mutex
	locks        map[string]*rowLock
	outboxMu     sync.Mutex
	shardID      string
}

const shardIdentityKey = "\x00moox/storage/shard-id"

func (s *Store) ShardID() string {
	if s == nil {
		return ""
	}
	return s.shardID
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
	shardID := strings.TrimSpace(opts.ShardID)
	if shardID == "" {
		return nil, errors.New("pebble shard id is required")
	}
	db, err := cpebble.Open(opts.Path, &cpebble.Options{})
	if err != nil {
		return nil, err
	}
	writeOptions := cpebble.Sync
	if opts.DisableSyncWrites {
		writeOptions = cpebble.NoSync
	}
	stored, closer, err := db.Get([]byte(shardIdentityKey))
	if err == nil {
		storedID := string(append([]byte(nil), stored...))
		_ = closer.Close()
		if storedID != shardID {
			_ = db.Close()
			return nil, fmt.Errorf("pebble shard identity conflict: stored=%q requested=%q", storedID, shardID)
		}
	} else if errors.Is(err, cpebble.ErrNotFound) {
		batch := db.NewBatch()
		if err := batch.Set([]byte(shardIdentityKey), []byte(shardID), cpebble.Sync); err != nil {
			_ = batch.Close()
			_ = db.Close()
			return nil, err
		}
		if err := batch.Commit(cpebble.Sync); err != nil {
			_ = batch.Close()
			_ = db.Close()
			return nil, err
		}
		_ = batch.Close()
	} else {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, writeOptions: writeOptions, locks: make(map[string]*rowLock), shardID: shardID}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) WriteRows(ctx context.Context, rows []*pb.ShardRow) error {
	return s.writeRows(ctx, rows, nil)
}

func (s *Store) DeleteRows(ctx context.Context, keys []*pb.ShardKey) error {
	return s.deleteRows(ctx, keys, nil)
}

func (s *Store) deleteRows(ctx context.Context, keys []*pb.ShardKey, entry *contracts.OutboxEntry) error {
	if s == nil || s.db == nil {
		return errors.New("pebble store is closed")
	}
	if len(keys) == 0 {
		return nil
	}
	for _, key := range keys {
		if err := validateKey(key); err != nil {
			return err
		}
	}
	rowKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		rowKeys = append(rowKeys, encodeShardKey(key))
	}
	unlock := s.lockRows(rowKeys)
	defer unlock()
	batch := s.db.NewBatch()
	defer batch.Close()
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if key == nil {
			continue
		}
		if err := batch.Delete([]byte(encodeShardKey(key)), s.writeOptions); err != nil {
			return err
		}
	}
	if entry != nil {
		s.outboxMu.Lock()
		defer s.outboxMu.Unlock()
		if err := s.stageOutbox(batch, entry); err != nil {
			return err
		}
	}
	return batch.Commit(s.writeOptions)
}

func (s *Store) writeRows(ctx context.Context, rows []*pb.ShardRow, entry *contracts.OutboxEntry) error {
	_ = ctx
	if len(rows) == 0 {
		return nil
	}
	if err := validateRowBatchIdentity(rows); err != nil {
		return err
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, encodeRowKey(row))
	}
	unlock := s.lockRows(keys)
	defer unlock()

	pending := make(map[string]*pb.ShardRow, len(rows))
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
	var sequence uint64
	if entry != nil {
		s.outboxMu.Lock()
		defer s.outboxMu.Unlock()
		var err error
		sequence, err = s.nextOutboxSequence()
		if err != nil {
			return err
		}
		for _, row := range pending {
			row.SourceShardId = s.shardID
			row.SourceSequence = sequence
			for _, column := range row.GetColumns() {
				if column != nil {
					column.SourceShardId = s.shardID
					column.SourceSequence = sequence
				}
			}
			for _, removal := range row.GetRemovedColumns() {
				if removal != nil {
					removal.SourceShardId = s.shardID
					removal.SourceSequence = sequence
				}
			}
		}
		if err := materializeRowsCommittedMessage(entry, rows, pending); err != nil {
			return err
		}
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
	if entry != nil {
		if err := s.stageOutboxAt(batch, entry, sequence); err != nil {
			return err
		}
	}
	return batch.Commit(s.writeOptions)
}

// materializeRowsCommittedMessage turns the caller's column patches into the
// complete post-merge snapshot that downstream consumers are entitled to
// receive. It runs after the pending rows have been merged and before the
// same Pebble batch stages the outbox entry, so the fact row and event cannot
// observe different states.
func materializeRowsCommittedMessage(entry *contracts.OutboxEntry, inputRows []*pb.ShardRow, pending map[string]*pb.ShardRow) error {
	if entry == nil || len(entry.Data) == 0 {
		return nil
	}
	msg := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(entry.Data, msg); err != nil {
		return fmt.Errorf("decode outbox message: %w", err)
	}
	lookup := make(map[string]*pb.ShardRow, len(pending))
	for key, row := range pending {
		lookup[key] = row
	}
	switch msg.GetMessageType() {
	case "moox.storage.time_series.rows_committed.v1":
		event := &pb.TimeSeriesRowsCommitted{}
		if err := proto.Unmarshal(msg.GetPayload(), event); err != nil {
			return fmt.Errorf("decode time-series rows committed payload: %w", err)
		}
		for idx, write := range event.GetWrites() {
			if write == nil || write.GetRow() == nil || idx >= len(inputRows) {
				continue
			}
			full := lookup[encodeRowKey(inputRows[idx])]
			if full == nil {
				continue
			}
			write.Row.Columns = cloneColumns(full.GetColumns())
			write.Row.Attributes = cloneAttributes(full.GetAttributes())
			write.Row.RemovedColumns = cloneColumnRemovals(full.GetRemovedColumns())
		}
		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
		if err != nil {
			return err
		}
		msg.Payload = data
	case "moox.storage.record.rows_committed.v1":
		event := &pb.RecordRowsCommitted{}
		if err := proto.Unmarshal(msg.GetPayload(), event); err != nil {
			return fmt.Errorf("decode record rows committed payload: %w", err)
		}
		for idx, write := range event.GetWrites() {
			if write == nil || write.GetRow() == nil || idx >= len(inputRows) {
				continue
			}
			full := lookup[encodeRowKey(inputRows[idx])]
			if full == nil {
				continue
			}
			write.Row.Columns = cloneColumns(full.GetColumns())
			write.Row.Attributes = cloneAttributes(full.GetAttributes())
			write.Row.RemovedColumns = cloneColumnRemovals(full.GetRemovedColumns())
		}
		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
		if err != nil {
			return err
		}
		msg.Payload = data
	default:
		return fmt.Errorf("unsupported outbox message_type %q", msg.GetMessageType())
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return err
	}
	entry.Data = data
	return nil
}

func cloneColumns(columns []*pb.ColumnValue) []*pb.ColumnValue {
	if len(columns) == 0 {
		return nil
	}
	out := make([]*pb.ColumnValue, 0, len(columns))
	for _, column := range columns {
		if column != nil {
			out = append(out, proto.Clone(column).(*pb.ColumnValue))
		}
	}
	return out
}

func cloneColumnRemovals(values []*pb.ColumnRemoval) []*pb.ColumnRemoval {
	if len(values) == 0 {
		return nil
	}
	out := make([]*pb.ColumnRemoval, 0, len(values))
	for _, value := range values {
		if value != nil {
			out = append(out, proto.Clone(value).(*pb.ColumnRemoval))
		}
	}
	return out
}

func cloneAttributes(attributes map[string]string) map[string]string {
	if len(attributes) == 0 {
		return nil
	}
	out := make(map[string]string, len(attributes))
	for key, value := range attributes {
		out[key] = value
	}
	return out
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

func (s *Store) getRow(key string) (*pb.ShardRow, error) {
	data, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	row := &pb.ShardRow{}
	if err := proto.Unmarshal(data, row); err != nil {
		return nil, err
	}
	return row, nil
}

func mergeRow(base *pb.ShardRow, patch *pb.ShardRow) *pb.ShardRow {
	if base == nil {
		merged := proto.Clone(patch).(*pb.ShardRow)
		for _, name := range patch.GetRemovedColumnNames() {
			merged.RemovedColumns = append(merged.RemovedColumns, &pb.ColumnRemoval{ColumnName: name})
		}
		merged.AttributesToDelete = nil
		merged.RemovedColumnNames = nil
		return merged
	}
	merged := proto.Clone(base).(*pb.ShardRow)
	merged.Key = proto.Clone(patch.GetKey()).(*pb.ShardKey)
	positions := make(map[string]int, len(merged.GetColumns()))
	for idx, column := range merged.GetColumns() {
		positions[column.GetColumnName()] = idx
	}
	for _, column := range patch.GetColumns() {
		merged.RemovedColumns = removeColumnRemoval(merged.RemovedColumns, column.GetColumnName())
		copied := proto.Clone(column).(*pb.ColumnValue)
		if idx, ok := positions[column.GetColumnName()]; ok {
			merged.Columns[idx] = copied
			continue
		}
		positions[column.GetColumnName()] = len(merged.Columns)
		merged.Columns = append(merged.Columns, copied)
	}
	for _, name := range patch.GetRemovedColumnNames() {
		if idx, ok := positions[name]; ok {
			merged.Columns = append(merged.Columns[:idx], merged.Columns[idx+1:]...)
			positions = columnPositions(merged.Columns)
		}
		merged.RemovedColumns = appendColumnRemoval(merged.RemovedColumns, &pb.ColumnRemoval{ColumnName: name, SourceShardId: patch.GetSourceShardId(), SourceSequence: patch.GetSourceSequence()})
	}
	for _, removal := range patch.GetRemovedColumns() {
		if removal == nil {
			continue
		}
		if idx, ok := positions[removal.GetColumnName()]; ok {
			merged.Columns = append(merged.Columns[:idx], merged.Columns[idx+1:]...)
			positions = columnPositions(merged.Columns)
		}
		merged.RemovedColumns = appendColumnRemoval(merged.RemovedColumns, proto.Clone(removal).(*pb.ColumnRemoval))
	}
	if len(patch.GetAttributes()) > 0 {
		if merged.Attributes == nil {
			merged.Attributes = make(map[string]string, len(patch.GetAttributes()))
		}
		for key, value := range patch.GetAttributes() {
			merged.Attributes[key] = value
		}
	}
	for _, key := range patch.GetAttributesToDelete() {
		delete(merged.Attributes, key)
	}
	merged.AttributesToDelete = nil
	merged.RemovedColumnNames = nil
	return merged
}

func removeColumnRemoval(values []*pb.ColumnRemoval, name string) []*pb.ColumnRemoval {
	filtered := values[:0]
	for _, value := range values {
		if value != nil && value.GetColumnName() != name {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func appendColumnRemoval(values []*pb.ColumnRemoval, removal *pb.ColumnRemoval) []*pb.ColumnRemoval {
	if removal == nil || removal.GetColumnName() == "" {
		return values
	}
	values = removeColumnRemoval(values, removal.GetColumnName())
	return append(values, removal)
}

func columnPositions(columns []*pb.ColumnValue) map[string]int {
	positions := make(map[string]int, len(columns))
	for idx, column := range columns {
		if column != nil {
			positions[column.GetColumnName()] = idx
		}
	}
	return positions
}

func (s *Store) ReadRows(ctx context.Context, keys []*pb.ShardKey, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
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
	if rows, result, ok, err := s.readRowsForSingleKeyPage(keys, versionRange, order, columnNames, page); ok {
		return rows, result, err
	}
	var rows []*pb.ShardRow
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

func (s *Store) readRowsForSingleKeyPage(keys []*pb.ShardKey, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, bool, error) {
	if len(keys) != 1 || order != pb.SortOrder_SORT_ORDER_DESC || page == nil || page.GetCursor() != "" {
		return nil, nil, false, nil
	}
	key := keys[0]
	if key.GetVersion() != "" {
		return nil, nil, false, nil
	}
	rows, result, err := s.readRowsForKeyDescPage(key, versionRange, columnNames, page)
	return rows, result, true, err
}

func (s *Store) ScanRows(ctx context.Context, target *pb.ShardTarget, dataKind pb.DataKind, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
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
	if page == nil {
		page = &pb.Page{}
	}
	prefix := []byte(encodeDatasetPrefix(dataKind, target.GetSpaceId(), target.GetDatasetId()))
	if order != pb.SortOrder_SORT_ORDER_DESC {
		return s.scanRowsForwardCursor(ctx, prefix, versionRange, columnNames, page)
	}
	return s.scanRowsReverseCursor(ctx, prefix, versionRange, columnNames, page)
}

func (s *Store) ScanRowsWithPrefix(ctx context.Context, target *pb.ShardTarget, dataKind pb.DataKind, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page, keyPrefix string) ([]*pb.ShardRow, *pb.PageResult, error) {
	if target == nil || target.GetSpaceId() == "" || target.GetDatasetId() == "" {
		return nil, nil, errors.New("target space_id and dataset_id are required")
	}
	if kindPrefix(dataKind) == "" || keyPrefix == "" {
		return nil, nil, errors.New("data_kind and key_prefix are required")
	}
	if page == nil {
		page = &pb.Page{}
	}
	prefix := []byte(encodeDatasetPrefix(dataKind, target.GetSpaceId(), target.GetDatasetId()) + keyPrefix)
	if order != pb.SortOrder_SORT_ORDER_DESC {
		return s.scanRowsForwardCursor(ctx, prefix, versionRange, columnNames, page)
	}
	return s.scanRowsReverseCursor(ctx, prefix, versionRange, columnNames, page)
}

func (s *Store) scanRowsReverseCursor(ctx context.Context, prefix []byte, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	_ = ctx
	upper := nextPrefix(prefix)
	if cursor := page.GetCursor(); cursor != "" {
		cursorBytes := []byte(cursor)
		if bytes.Compare(cursorBytes, prefix) >= 0 && (len(upper) == 0 || bytes.Compare(cursorBytes, upper) < 0) {
			upper = cursorBytes
		}
	}
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()

	size := pageSize(page)
	rows := make([]*pb.ShardRow, 0, size)
	nextCursor := ""
	hasMore := false
	for valid := iter.Last(); valid; valid = iter.Prev() {
		encoded := string(iter.Key())
		if !versionRangeContains(encodedRowVersion(encoded), versionRange) {
			continue
		}
		if uint32(len(rows)) >= size {
			hasMore = true
			break
		}
		row := &pb.ShardRow{}
		if err := proto.Unmarshal(iter.Value(), row); err != nil {
			return nil, nil, err
		}
		rows = append(rows, filterRowColumns(row, columnNames))
		nextCursor = encoded
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	if !hasMore {
		nextCursor = ""
	}
	return rows, &pb.PageResult{Size: size, HasMore: hasMore, NextCursor: nextCursor}, nil
}

func (s *Store) scanRowsForwardCursor(ctx context.Context, prefix []byte, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	_ = ctx
	size := pageSize(page)
	lower := prefix
	upper := nextPrefix(prefix)
	if cursor := page.GetCursor(); cursor != "" {
		cursorBytes := []byte(cursor)
		if bytes.Compare(cursorBytes, prefix) >= 0 && (len(upper) == 0 || bytes.Compare(cursorBytes, upper) < 0) {
			if next := nextPrefix(cursorBytes); len(next) > 0 {
				lower = next
			}
		}
	}
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()

	rows := make([]*pb.ShardRow, 0, size)
	nextCursor := ""
	hasMore := false
	for valid := iter.First(); valid; valid = iter.Next() {
		encoded := string(iter.Key())
		if !versionRangeContains(encodedRowVersion(encoded), versionRange) {
			continue
		}
		if uint32(len(rows)) >= size {
			hasMore = true
			break
		}
		row := &pb.ShardRow{}
		if err := proto.Unmarshal(iter.Value(), row); err != nil {
			return nil, nil, err
		}
		rows = append(rows, filterRowColumns(row, columnNames))
		nextCursor = encoded
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	if !hasMore {
		nextCursor = ""
	}
	return rows, &pb.PageResult{
		Size:       size,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	}, nil
}

func canReadExact(keys []*pb.ShardKey, versionRange *pb.VersionRange, page *pb.Page) bool {
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

func (s *Store) readExactRows(keys []*pb.ShardKey, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	rows := make([]*pb.ShardRow, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		encoded := encodeShardKey(key)
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

func (s *Store) readRowsForKey(key *pb.ShardKey, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.ShardRow, error) {
	if key.GetVersion() != "" && versionRange == nil {
		row, err := s.getRow(encodeShardKey(key))
		if err != nil || row == nil {
			return nil, err
		}
		return []*pb.ShardRow{filterRowColumns(row, columnNames)}, nil
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
	var rows []*pb.ShardRow
	for valid := iter.First(); valid; valid = iter.Next() {
		row := &pb.ShardRow{}
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

func (s *Store) readRowsForKeyDescPage(key *pb.ShardKey, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	lower, upper := keyBounds(key, versionRange)
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, nil, err
	}
	defer iter.Close()

	pageNo := uint32(1)
	if page.GetPage() > 0 {
		pageNo = page.GetPage()
	}
	size := pageSize(page)
	skip := int((pageNo - 1) * size)
	rows := make([]*pb.ShardRow, 0, size)
	hasMore := false
	for valid := iter.Last(); valid; valid = iter.Prev() {
		if skip > 0 {
			skip--
			continue
		}
		if uint32(len(rows)) >= size {
			hasMore = true
			break
		}
		row := &pb.ShardRow{}
		if err := proto.Unmarshal(iter.Value(), row); err != nil {
			return nil, nil, err
		}
		rows = append(rows, filterRowColumns(row, columnNames))
	}
	if err := iter.Error(); err != nil {
		return nil, nil, err
	}
	return rows, &pb.PageResult{
		Page:       pageNo,
		Size:       size,
		HasMore:    hasMore,
		TotalState: pb.TotalState_SKIPPED,
	}, nil
}

func validateRow(row *pb.ShardRow) error {
	if row == nil {
		return errors.New("row is required")
	}
	return validateKey(row.GetKey())
}

func validateKey(key *pb.ShardKey) error {
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

func encodedRowVersion(encoded string) string {
	parts := strings.Split(encoded, "|")
	if len(parts) < 5 {
		return ""
	}
	return unescape(parts[len(parts)-1])
}

func unescape(value string) string {
	value = strings.ReplaceAll(value, "%7C", "|")
	value = strings.ReplaceAll(value, "%25", "%")
	return value
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

func filterRowColumns(row *pb.ShardRow, includes []string) *pb.ShardRow {
	if len(includes) == 0 {
		return row
	}
	allow := makeSet(includes)
	filtered := proto.Clone(row).(*pb.ShardRow)
	filtered.Columns = filtered.Columns[:0]
	for _, column := range row.GetColumns() {
		if allow[column.GetColumnName()] {
			filtered.Columns = append(filtered.Columns, column)
		}
	}
	return filtered
}

func sortRows(rows []*pb.ShardRow, order pb.SortOrder) {
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

func pageRows(rows []*pb.ShardRow, page *pb.Page, order pb.SortOrder) ([]*pb.ShardRow, *pb.PageResult) {
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
			Total:      uint32(len(rows)),
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
		Total:      uint32(len(rows)),
		HasMore:    end < len(rows),
		NextCursor: next,
	}
}

func cursorStart(rows []*pb.ShardRow, cursor string, order pb.SortOrder) int {
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
