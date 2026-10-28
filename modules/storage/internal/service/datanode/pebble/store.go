package pebble

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/proto"
)

const (
	outboxPrefix         = "__outbox/"
	datasetDeletedPrefix = "__dataset_deleted/"
	metaNextID           = "__meta/next_outbox_id"
)

// Options controls one DataNode Pebble database. A node can host multiple
// datasets; the dataset identity is part of every physical key.
type Options struct {
	Path           string
	NodeID         string
	BucketDuration time.Duration
	MaxEventBytes  int
	// ChangeLogRetention 是行变更日志的保留期；为 0 时取 DefaultChangeLogRetention。
	ChangeLogRetention time.Duration
	DisableSyncWrites  bool
	PeriodNow          func() time.Time
	// BlockCacheBytes 是 Pebble 块缓存大小；为 0 时由环境变量或主机内存推算，见 resolveBlockCache。
	BlockCacheBytes int64
}

type OutboxEntry struct {
	ID        uint64
	Data      []byte
	CreatedAt time.Time
}

type OutboxStats struct {
	Pending       int
	OldestEventAt time.Time
}

type Store struct {
	db                      *cpebble.DB
	writeOptions            *cpebble.WriteOptions
	nodeID                  string
	sourceStoreID           string
	bucketDuration          time.Duration
	maxEventBytes           int
	changeLogRetention      time.Duration
	datasetWriteMu          sync.RWMutex
	outboxMu                sync.Mutex
	outboxPublicationMu     sync.Mutex
	outboxPublicationLocks  map[outboxDatasetScope]*outboxPublicationLock
	periodMu                sync.Mutex
	periodFinalizeMu        sync.Mutex
	periodNow               func() time.Time
	periodFinalizeErrorMu   sync.Mutex
	periodFinalizeLastError string
	outboxPending           atomic.Int64
	outboxRevision          atomic.Uint64
	// outboxWake 在 outbox 有新条目提交后收到一个信号（容量 1，合并），转发协程据此立即发布，不等轮询。
	outboxWake             chan struct{}
	outboxHintKnown        atomic.Bool
	outboxHintMayHaveMore  atomic.Bool
	outboxOldestEventNanos atomic.Int64
	maintenanceMu          sync.Mutex
	maintenanceWG          sync.WaitGroup
	backgroundCtx          context.Context
	backgroundCancel       context.CancelFunc
	closing                bool
	blockCacheBytes        int64
	// statsStop 通知缓存用量日志协程退出，由 Close 关闭。
	statsStop chan struct{}
	statsOnce sync.Once
}

func Open(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("pebble path is required")
	}
	if strings.TrimSpace(opts.NodeID) == "" || strings.TrimSpace(opts.NodeID) != opts.NodeID {
		return nil, errors.New("node_id is required")
	}
	if opts.BucketDuration <= 0 {
		opts.BucketDuration = 24 * time.Hour
	}
	if opts.PeriodNow == nil {
		opts.PeriodNow = time.Now
	}
	if err := ensureLayout(opts.Path); err != nil {
		return nil, err
	}
	cacheBytes, cacheSource := resolveBlockCache(opts.BlockCacheBytes)
	cache := cpebble.NewCache(cacheBytes)
	// Open 会自己持有一份引用；这里放掉创建时的那份，DB 关闭时缓存随之释放。
	defer cache.Unref()
	db, err := cpebble.Open(opts.Path, &cpebble.Options{Merger: BitmapORMerger, Cache: cache})
	if err != nil {
		return nil, err
	}
	cacheLogf("storage node Pebble 块缓存 %dMB（%s）", cacheBytes>>20, cacheSource)
	sourceStoreID, err := bindSourceStore(db, opts.NodeID)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	writeOptions := cpebble.Sync
	if opts.DisableSyncWrites {
		writeOptions = cpebble.NoSync
	}
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	store := &Store{db: db, writeOptions: writeOptions, nodeID: opts.NodeID, sourceStoreID: sourceStoreID, bucketDuration: opts.BucketDuration, maxEventBytes: opts.MaxEventBytes, changeLogRetention: opts.ChangeLogRetention, periodNow: opts.PeriodNow, backgroundCtx: backgroundCtx, backgroundCancel: backgroundCancel, blockCacheBytes: cacheBytes, statsStop: make(chan struct{}), outboxWake: make(chan struct{}, 1)}
	store.startPeriodFinalizer()
	store.startCacheStats(cacheStatsFirstDelay, cacheStatsInterval)
	return store, nil
}

// DiskSpaceUsage returns the on-disk size of the store, including files that
// compaction has made obsolete but not yet deleted.
func (s *Store) DiskSpaceUsage() uint64 {
	if s == nil || s.db == nil {
		return 0
	}
	return s.db.Metrics().DiskSpaceUsage()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.maintenanceMu.Lock()
	s.closing = true
	s.maintenanceMu.Unlock()
	s.statsOnce.Do(func() {
		if s.statsStop != nil {
			close(s.statsStop)
		}
	})
	if s.backgroundCancel != nil {
		s.backgroundCancel()
	}
	s.maintenanceWG.Wait()
	return s.db.Close()
}

func (s *Store) compactAsync(start, end []byte) {
	s.maintenanceMu.Lock()
	if s.closing {
		s.maintenanceMu.Unlock()
		return
	}
	s.maintenanceWG.Add(1)
	s.maintenanceMu.Unlock()
	go func() {
		defer s.maintenanceWG.Done()
		_ = s.db.Compact(start, end, true)
	}()
}

func (s *Store) NodeID() string {
	if s == nil {
		return ""
	}
	return s.nodeID
}

func validateUpsert(row *pb.RowFieldUpsert) error {
	if row == nil || row.GetKey() == nil {
		return invalid("row key is required")
	}
	if len(row.GetFields()) == 0 && len(row.GetAttributes()) == 0 {
		return invalid("at least one field or attribute is required")
	}
	seen := make(map[string]struct{}, len(row.GetFields()))
	for _, field := range row.GetFields() {
		if field == nil || field.GetFieldId() == "" || field.GetValue() == nil {
			return invalid("field_id and value are required")
		}
		if _, ok := seen[field.GetFieldId()]; ok {
			return invalidf("duplicate field_id %q", field.GetFieldId())
		}
		seen[field.GetFieldId()] = struct{}{}
	}
	for name, value := range row.GetAttributes() {
		if name == "" || value == nil {
			return invalid("attribute key and value are required")
		}
	}
	return nil
}

// pebbleReader 是 *pebble.DB 与 *pebble.Snapshot 共有的只读接口。
type pebbleReader interface {
	Get(key []byte) ([]byte, io.Closer, error)
	NewIter(o *cpebble.IterOptions) (*cpebble.Iterator, error)
}

func (s *Store) resolveMaxRecordVersion(reader pebbleReader, key *pb.RowKey) (*pb.RowKey, error) {
	// The tuple codec preserves UTF-8 byte order, so the last key under each
	// namespace prefix contains that namespace's largest record version.
	base, err := recordBasePrefix(key)
	if err != nil {
		return nil, err
	}
	max := ""
	for _, namespace := range []byte{fieldNamespace, attributeNamespace} {
		iter, err := reader.NewIter(&cpebble.IterOptions{LowerBound: append([]byte{namespace}, base...), UpperBound: nextPrefix(append([]byte{namespace}, base...))})
		if err != nil {
			return nil, err
		}
		if valid := iter.Last(); valid {
			parts, ok := parseRecordValueKey(iter.Key())
			if ok && parts.version > max {
				max = parts.version
			}
		}
		iterErr := iter.Error()
		_ = iter.Close()
		if iterErr != nil {
			return nil, iterErr
		}
	}
	if max == "" {
		return key, nil
	}
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: key.GetRecord().GetRecordId(), Version: max}}}, nil
}

func recordBasePrefix(key *pb.RowKey) ([]byte, error) {
	record := key.GetRecord()
	if record == nil || record.GetRecordId() == "" {
		return nil, invalid("record key is required")
	}
	out := []byte{recordKind}
	out = appendRawPart(out, []byte(key.GetSpaceId()))
	out = appendRawPart(out, []byte(key.GetDatasetId()))
	out = appendRawPart(out, []byte(record.GetRecordId()))
	return out, nil
}

type recordKeyParts struct{ version string }

func parseRecordValueKey(key []byte) (recordKeyParts, bool) {
	if len(key) < 2 || (key[0] != fieldNamespace && key[0] != attributeNamespace) || key[1] != recordKind {
		return recordKeyParts{}, false
	}
	rest := key[2:]
	for i := 0; i < 3; i++ {
		_, next, err := decodePart(rest)
		if err != nil {
			return recordKeyParts{}, false
		}
		rest = next
	}
	version, _, err := decodePart(rest)
	if err == nil {
		return recordKeyParts{version: version}, true
	}
	return recordKeyParts{}, false
}

func (s *Store) nextOutboxID() (uint64, error) {
	data, closer, err := s.db.Get([]byte(metaNextID))
	if errors.Is(err, cpebble.ErrNotFound) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	if len(data) != 8 {
		return 0, errors.New("invalid next_outbox_id")
	}
	id := binary.BigEndian.Uint64(data)
	if id == 0 {
		return 1, nil
	}
	return id, nil
}

func (s *Store) setNextOutboxID(batch *cpebble.Batch, id uint64) error {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], id)
	return batch.Set([]byte(metaNextID), data[:], s.writeOptions)
}

func outboxKey(id uint64) string { return fmt.Sprintf("%s%020d", outboxPrefix, id) }

// PrepareOutboxPublication validates and returns the exact persisted event
// bytes. EventMessage has no mutable published_at field, so retries always
// reuse the same stable event_id and payload.
func (s *Store) PrepareOutboxPublication(ctx context.Context, id uint64, now time.Time) ([]byte, error) {
	_ = now
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, errors.New("outbox id is required")
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	return s.prepareOutboxPublicationLocked(ctx, id)
}

// PublishOutboxPublication holds a dataset-scoped fence through the external
// publish acknowledgement. Local outbox mutations use outboxMu only while
// touching Pebble, so a slow broker cannot block writes to other datasets.
func (s *Store) PublishOutboxPublication(ctx context.Context, id uint64, publish func([]byte) error) error {
	if s == nil || s.db == nil {
		return errors.New("pebble store is closed")
	}
	if publish == nil {
		return errors.New("outbox publisher callback is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if id == 0 {
		return errors.New("outbox id is required")
	}
	data, err := s.PrepareOutboxPublication(ctx, id, time.Time{})
	if err != nil {
		return err
	}
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		return fmt.Errorf("unmarshal outbox entry %d: %w", id, err)
	}
	spaceID, datasetID := strings.TrimSpace(message.GetSpaceId()), strings.TrimSpace(message.GetSubjectId())
	if spaceID == "" || datasetID == "" {
		return fmt.Errorf("outbox entry %d has no dataset scope", id)
	}
	unlock, err := s.lockOutboxDatasetPublication(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	defer unlock()
	data, err = s.PrepareOutboxPublication(ctx, id, time.Time{})
	if err != nil {
		return err
	}
	return publish(data)
}

type outboxDatasetScope struct{ spaceID, datasetID string }

type outboxPublicationLock struct {
	// slot 容量为 1 的信号量：与 sync.Mutex 不同，等待者可以响应 context 取消
	slot chan struct{}
	refs int
}

// lockOutboxDatasetPublication 获取 Dataset 级发布锁；等待期间 ctx 取消立即返回并回收引用。
func (s *Store) lockOutboxDatasetPublication(ctx context.Context, spaceID, datasetID string) (func(), error) {
	scope := outboxDatasetScope{spaceID: spaceID, datasetID: datasetID}
	s.outboxPublicationMu.Lock()
	if s.outboxPublicationLocks == nil {
		s.outboxPublicationLocks = make(map[outboxDatasetScope]*outboxPublicationLock)
	}
	lock := s.outboxPublicationLocks[scope]
	if lock == nil {
		lock = &outboxPublicationLock{slot: make(chan struct{}, 1)}
		s.outboxPublicationLocks[scope] = lock
	}
	lock.refs++
	s.outboxPublicationMu.Unlock()

	release := func() {
		s.outboxPublicationMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.outboxPublicationLocks, scope)
		}
		s.outboxPublicationMu.Unlock()
	}
	select {
	case lock.slot <- struct{}{}:
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
	return func() {
		<-lock.slot
		release()
	}, nil
}

func (s *Store) prepareOutboxPublicationLocked(ctx context.Context, id uint64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, closer, err := s.db.Get([]byte(outboxKey(id)))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, fmt.Errorf("%w: %d", ErrOutboxEntryNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	raw := append([]byte(nil), data...)
	if err := closer.Close(); err != nil {
		return nil, err
	}
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, message); err != nil {
		return nil, fmt.Errorf("unmarshal outbox entry %d: %w", id, err)
	}
	if _, err := validateDataNodeMarkerMessage(raw); err != nil {
		return nil, fmt.Errorf("%w %d: %v", ErrInvalidOutboxEvent, id, err)
	}
	return raw, nil
}

func (s *Store) ListOutbox(ctx context.Context, after uint64, max int) ([]*OutboxEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if max <= 0 {
		max = 100
	}
	revision := s.outboxRevision.Load()
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: []byte(outboxPrefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	result := make([]*OutboxEntry, 0, max)
	var oldestEventAt time.Time
	for valid := iter.First(); valid && len(result) < max; valid = iter.Next() {
		name := strings.TrimPrefix(string(iter.Key()), outboxPrefix)
		var id uint64
		if _, err := fmt.Sscanf(name, "%d", &id); err != nil || id <= after {
			continue
		}
		data := append([]byte(nil), iter.Value()...)
		result = append(result, &OutboxEntry{ID: id, Data: data})
		message := &eventpb.EventMessage{}
		if unmarshalErr := proto.Unmarshal(data, message); unmarshalErr == nil {
			if occurred := message.GetOccurredAt(); occurred != nil && occurred.CheckValid() == nil {
				eventAt := occurred.AsTime()
				if oldestEventAt.IsZero() || eventAt.Before(oldestEventAt) {
					oldestEventAt = eventAt
				}
			}
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	// Keep the relay's hot path on a cheap atomic hint. A full scan is still
	// used to discover entries that existed before a restart, but it must not be
	// repeated every poll when the outbox is empty. Do not overwrite a concurrent write.
	s.applyOutboxHintIfUnchanged(revision, func() { s.setOutboxHintFromList(len(result), len(result) == max, oldestEventAt) })
	return result, nil
}

// OutboxStats 扫描已提交 outbox 记录，供 relay 汇报完整等待数量，而非仅汇报
// 单次发布批次。时间戳解码失败不改变 relay 行为；下一条记录仍由
// PrepareOutboxPublication 负责权威校验。
func (s *Store) OutboxStats(ctx context.Context) (OutboxStats, error) {
	if err := ctx.Err(); err != nil {
		return OutboxStats{}, err
	}
	revision := s.outboxRevision.Load()
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(outboxPrefix), UpperBound: []byte(outboxPrefix + "\xff")})
	if err != nil {
		return OutboxStats{}, err
	}
	defer iter.Close()
	stats := OutboxStats{}
	for valid := iter.First(); valid; valid = iter.Next() {
		if err := ctx.Err(); err != nil {
			return OutboxStats{}, err
		}
		stats.Pending++
		message := &eventpb.EventMessage{}
		if err := proto.Unmarshal(iter.Value(), message); err != nil {
			continue
		}
		var eventAt time.Time
		if occurred := message.GetOccurredAt(); occurred != nil && occurred.CheckValid() == nil {
			eventAt = occurred.AsTime()
		}
		if !eventAt.IsZero() && (stats.OldestEventAt.IsZero() || eventAt.Before(stats.OldestEventAt)) {
			stats.OldestEventAt = eventAt
		}
	}
	if err := iter.Error(); err != nil {
		return OutboxStats{}, err
	}
	s.applyOutboxHintIfUnchanged(revision, func() { s.setOutboxHintFromStats(stats) })
	return stats, nil
}

// InsertOutboxPayloadForTest writes raw EventMessage bytes without validating
// the current outbox contract. Tests use it to stage retired event types.
func (s *Store) InsertOutboxPayloadForTest(raw []byte) (uint64, error) {
	if len(raw) == 0 {
		return 0, errors.New("outbox payload is required")
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	nextID, err := s.nextOutboxID()
	if err != nil {
		return 0, err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(outboxKey(nextID)), raw, s.writeOptions); err != nil {
		return 0, err
	}
	if err := s.setNextOutboxID(batch, nextID+1); err != nil {
		return 0, err
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return 0, err
	}
	s.noteOutboxCommitted(1, time.Now().UTC())
	return nextID, nil
}

func (s *Store) DeleteOutbox(ctx context.Context, ids []uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	// 只统计实际存在、去重后的删除项：同一批 id 可能已被并发的数据集清理删除，或被 relay 重复删除；按 len(ids) 扣减会把其他
	// 事件的待发计数扣成零，hint 误判队列为空后 relay 不再扫描，新事件一直滞留。
	sorted := make([]uint64, 0, len(ids))
	seen := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate || id == 0 {
			continue
		}
		seen[id] = struct{}{}
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	batch := s.db.NewBatch()
	defer batch.Close()
	existing := 0
	for _, id := range sorted {
		key := []byte(outboxKey(id))
		_, closer, err := s.db.Get(key)
		if errors.Is(err, cpebble.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		_ = closer.Close()
		existing++
		if err := batch.Delete(key, s.writeOptions); err != nil {
			return err
		}
	}
	if existing == 0 {
		return nil
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return err
	}
	s.noteOutboxDeleted(existing)
	return nil
}

// OutboxPendingHint returns a lock-free lower-cost view of the outbox state.
// It is maintained by the atomic Pebble write/delete boundaries and is used by
// the relay to avoid constructing a Pebble iterator on every empty poll.
func (s *Store) OutboxPendingHint() (pending int, known, mayHaveMore bool, oldestEventAt time.Time) {
	if s == nil {
		return 0, false, false, time.Time{}
	}
	return int(maxInt64(s.outboxPending.Load(), 0)), s.outboxHintKnown.Load(), s.outboxHintMayHaveMore.Load(), unixNanoTime(s.outboxOldestEventNanos.Load())
}

// applyOutboxHintIfUnchanged 在与写入/删除共用的 outboxMu 下复核 revision 并回写缓存：扫描读到“空”之后、回写之前，
// 并发写入可能已经提交事件并推进 revision；不加锁的“先检查再回写”会用过期的“空队列”覆盖掉它，relay 之后一直
// 跳过扫描，已提交的事件不再发布。写入在持有 outboxMu 的情况下提交并推进 revision，所以在这里复核是原子的。
func (s *Store) applyOutboxHintIfUnchanged(revision uint64, apply func()) {
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if revision == s.outboxRevision.Load() {
		apply()
	}
}

// OutboxWake 返回 outbox 提交通知：每次成功提交了 outbox 条目后收到一个信号（多次提交合并为一个）。
func (s *Store) OutboxWake() <-chan struct{} { return s.outboxWake }

func (s *Store) noteOutboxCommitted(count int, oldest time.Time) {
	if count <= 0 {
		return
	}
	// Keep the hint unknown until the first bounded scan after a restart. A
	// newly committed event must not hide entries that already existed in the
	// database before the restart.
	s.outboxPending.Add(int64(count))
	if !oldest.IsZero() {
		oldestNanos := oldest.UTC().UnixNano()
		for {
			current := s.outboxOldestEventNanos.Load()
			if current != 0 && current <= oldestNanos {
				break
			}
			if s.outboxOldestEventNanos.CompareAndSwap(current, oldestNanos) {
				break
			}
		}
	}
	s.outboxRevision.Add(1)
	select {
	case s.outboxWake <- struct{}{}:
	default:
	}
}

func (s *Store) noteOutboxDeleted(count int) {
	if count <= 0 {
		return
	}
	if s.outboxHintKnown.Load() {
		pending := s.outboxPending.Add(-int64(count))
		if pending < 0 {
			s.outboxPending.Store(0)
		}
		if s.outboxPending.Load() == 0 && !s.outboxHintMayHaveMore.Load() {
			s.outboxOldestEventNanos.Store(0)
		}
	}
	s.outboxRevision.Add(1)
}

func (s *Store) setOutboxHintFromList(count int, mayHaveMore bool, oldestEventAt time.Time) {
	pendingLowerBound := count
	if mayHaveMore {
		pendingLowerBound++
	}
	s.outboxPending.Store(int64(pendingLowerBound))
	s.outboxHintKnown.Store(true)
	s.outboxHintMayHaveMore.Store(mayHaveMore)
	if count == 0 {
		s.outboxOldestEventNanos.Store(0)
	} else if !oldestEventAt.IsZero() {
		s.outboxOldestEventNanos.Store(oldestEventAt.UTC().UnixNano())
	}
}

func (s *Store) setOutboxHintFromStats(stats OutboxStats) {
	s.outboxPending.Store(int64(stats.Pending))
	s.outboxHintKnown.Store(true)
	s.outboxHintMayHaveMore.Store(false)
	if stats.OldestEventAt.IsZero() {
		s.outboxOldestEventNanos.Store(0)
	} else {
		s.outboxOldestEventNanos.Store(stats.OldestEventAt.UTC().UnixNano())
	}
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func unixNanoTime(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value).UTC()
}
