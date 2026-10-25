package pebble

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	outboxPrefix             = "__outbox/"
	datasetDeletedPrefix     = "__dataset_deleted/"
	processedEventPrefix     = "__processed_event/"
	processedEventTimePrefix = "__processed_event_time/"
	metaNextID               = "__meta/next_outbox_id"
	// The market stream keeps source events for 168 hours. Keep dedupe markers
	// one day longer so a late redelivery cannot recreate a row after stream
	// retention has elapsed.
	defaultProcessedEventRetention = 192 * time.Hour
	processedEventCleanupBatchSize = 256
)

// Options controls one DataNode Pebble database. A node can host multiple
// datasets; the dataset identity is part of every physical key.
type Options struct {
	Path                    string
	NodeID                  string
	BucketDuration          time.Duration
	MaxEventBytes           int
	ProcessedEventRetention time.Duration
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
	processedEventRetention time.Duration
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
	outboxHintKnown         atomic.Bool
	outboxHintMayHaveMore   atomic.Bool
	outboxOldestEventNanos  atomic.Int64
	maintenanceMu           sync.Mutex
	maintenanceWG           sync.WaitGroup
	backgroundCtx           context.Context
	backgroundCancel        context.CancelFunc
	closing                 bool
	blockCacheBytes         int64
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
	if opts.ProcessedEventRetention <= 0 {
		opts.ProcessedEventRetention = defaultProcessedEventRetention
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
	store := &Store{db: db, writeOptions: writeOptions, nodeID: opts.NodeID, sourceStoreID: sourceStoreID, bucketDuration: opts.BucketDuration, maxEventBytes: opts.MaxEventBytes, processedEventRetention: opts.ProcessedEventRetention, changeLogRetention: opts.ChangeLogRetention, periodNow: opts.PeriodNow, backgroundCtx: backgroundCtx, backgroundCancel: backgroundCancel, blockCacheBytes: cacheBytes, statsStop: make(chan struct{})}
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

// ProcessedEventRetention returns the configured source-event dedupe window.
func (s *Store) ProcessedEventRetention() time.Duration {
	if s == nil || s.processedEventRetention <= 0 {
		return defaultProcessedEventRetention
	}
	return s.processedEventRetention
}

// UpsertFields upserts each Field/Attribute independently. All values and the
// corresponding event are committed in one Pebble batch by UpsertFieldsEvent.
func (s *Store) UpsertFields(ctx context.Context, rows []*pb.RowFieldUpsert) error {
	_, err := s.UpsertFieldsEvent(ctx, rows, nil)
	return err
}

// UpsertFieldsEvent 为本次已提交写入涉及的每个 Dataset 返回一条事件载荷。
// event 可为空，本地写入调用方因此不会创建 outbox 记录。
func (s *Store) UpsertFieldsEvent(ctx context.Context, rows []*pb.RowFieldUpsert, event func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error)) ([]*OutboxEntry, error) {
	return s.writeFieldsEvent(ctx, rows, "", event)
}

// UpsertFieldsEventWithSource atomically records a source EventMessage ID with
// the row mutation and its outbox entry. A redelivery after a successful write
// and failed ACK therefore becomes a no-op instead of creating a second
// DatasetRowsUpserted event.
func (s *Store) UpsertFieldsEventWithSource(ctx context.Context, rows []*pb.RowFieldUpsert, sourceEventID string, event func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error)) ([]*OutboxEntry, error) {
	if strings.TrimSpace(sourceEventID) == "" {
		return nil, invalid("source_event_id is required")
	}
	if event == nil {
		return nil, invalid("event builder is required for source-id writes")
	}
	return s.writeFieldsEvent(ctx, rows, sourceEventID, event)
}

func (s *Store) writeFieldsEvent(ctx context.Context, rows []*pb.RowFieldUpsert, sourceEventID string, event func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error)) ([]*OutboxEntry, error) {
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	normalizedRows, err := s.normalizeWriteRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	if sourceEventID != "" {
		// 带来源事件 ID 的写入要在锁内判重（并发重投只能写一次），整条路径都在锁内。
		s.outboxMu.Lock()
		defer s.outboxMu.Unlock()
		return s.writeFieldsEventLocked(ctx, normalizedRows, sourceEventID, true, event, nil)
	}
	// 编码 KV、构造并校验事件与锁无关，放在锁外；锁内只分配 outbox 编号、写变更日志并提交，
	// 编号与提交顺序仍由 outboxMu 保证一致。
	prepared, err := s.prepareFieldsWrite(ctx, groupRowsByDataset(normalizedRows), normalizedRows, event)
	if err != nil {
		return nil, err
	}
	defer prepared.batch.Close()
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	return s.commitFieldsWriteLocked(prepared, "", true, nil)
}

func (s *Store) normalizeWriteRows(ctx context.Context, rows []*pb.RowFieldUpsert) ([]*pb.RowFieldUpsert, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, invalid("rows are required")
	}
	normalizedRows := make([]*pb.RowFieldUpsert, 0, len(rows))
	for _, row := range rows {
		if err := validateUpsert(row); err != nil {
			return nil, err
		}
		key, err := NormalizeRowKey(row.GetKey())
		if err != nil {
			return nil, err
		}
		clone := proto.Clone(row).(*pb.RowFieldUpsert)
		clone.Key = key
		normalizedRows = append(normalizedRows, clone)
	}
	return normalizedRows, nil
}

// changeLog 为真时，在同一个 batch 里追加行变更日志（因子结果写入不记：它们不是本地副本的输入）。
func (s *Store) writeFieldsEventLocked(ctx context.Context, normalizedRows []*pb.RowFieldUpsert, sourceEventID string, changeLog bool, event func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error), decorate func(*cpebble.Batch, []*OutboxEntry) error) ([]*OutboxEntry, error) {
	grouped := groupRowsByDataset(normalizedRows)
	if sourceEventID != "" {
		// 已删除的数据集先于判重报错：重投一条已处理的事件也要知道数据集没了。
		for group := range grouped {
			deleted, err := s.isDatasetDeleted(group.spaceID, group.datasetID)
			if err != nil {
				return nil, err
			}
			if deleted {
				return nil, ErrDatasetDeleted
			}
		}
		pending := make(map[datasetGroup][]*pb.RowFieldUpsert, len(grouped))
		for group, groupRows := range grouped {
			processed, err := s.hasProcessedSourceEvent(sourceEventID, group)
			if err != nil {
				return nil, err
			}
			if !processed {
				pending[group] = groupRows
			}
		}
		grouped = pending
		if len(grouped) == 0 {
			return nil, nil
		}
		normalizedRows = make([]*pb.RowFieldUpsert, 0, len(normalizedRows))
		for _, groupRows := range grouped {
			normalizedRows = append(normalizedRows, groupRows...)
		}
	}
	prepared, err := s.prepareFieldsWrite(ctx, grouped, normalizedRows, event)
	if err != nil {
		return nil, err
	}
	defer prepared.batch.Close()
	return s.commitFieldsWriteLocked(prepared, sourceEventID, changeLog, decorate)
}

// preparedFieldsWrite 是一次写入在锁外准备好的部分：已写入字段、属性与行索引 KV 的 batch，以及每个数据集
// 构造并校验过、还没绑定 outbox 编号的事件。
type preparedFieldsWrite struct {
	batch    *cpebble.Batch
	grouped  map[datasetGroup][]*pb.RowFieldUpsert
	payloads map[datasetGroup][]byte
}

// prepareFieldsWrite 只读 Store 的静态配置，不读写 outbox 与变更序号，可以在 outboxMu 之外并发执行。
func (s *Store) prepareFieldsWrite(ctx context.Context, grouped map[datasetGroup][]*pb.RowFieldUpsert, normalizedRows []*pb.RowFieldUpsert, event func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error)) (*preparedFieldsWrite, error) {
	// One batch preserves all field changes from a request. Dataset event
	// payloads and source-event markers are staged into the same batch.
	batch := s.db.NewBatch()
	prepared := &preparedFieldsWrite{batch: batch, grouped: grouped}
	fail := func(err error) (*preparedFieldsWrite, error) {
		_ = batch.Close()
		return nil, err
	}
	for _, row := range normalizedRows {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		// Keep a single logical row marker alongside the field/attribute values.
		// The marker is an ordered, time-first index used by history backfills;
		// writing it in the same batch makes a committed row immediately visible
		// to the history reader.
		if row.GetKey().GetTimeSeries() != nil {
			historyKey, err := encodeHistoryKey(row.GetKey(), s.bucketDuration)
			if err != nil {
				return fail(err)
			}
			if err := batch.Set(historyKey, nil, s.writeOptions); err != nil {
				return fail(err)
			}
			seriesHistoryKey, err := encodeSeriesHistoryKey(row.GetKey(), s.bucketDuration)
			if err != nil {
				return fail(err)
			}
			if err := batch.Set(seriesHistoryKey, nil, s.writeOptions); err != nil {
				return fail(err)
			}
		}
		for _, field := range row.GetFields() {
			key, err := encodeFieldKey(row.GetKey(), field.GetFieldId(), s.bucketDuration)
			if err != nil {
				return fail(err)
			}
			data, err := proto.Marshal(field.GetValue())
			if err != nil {
				return fail(fmt.Errorf("marshal field %q: %w", field.GetFieldId(), err))
			}
			if err := batch.Set(key, data, s.writeOptions); err != nil {
				return fail(err)
			}
		}
		for name, value := range row.GetAttributes() {
			key, err := encodeAttributeKey(row.GetKey(), name, s.bucketDuration)
			if err != nil {
				return fail(err)
			}
			data, err := proto.Marshal(value)
			if err != nil {
				return fail(fmt.Errorf("marshal attribute %q: %w", name, err))
			}
			if err := batch.Set(key, data, s.writeOptions); err != nil {
				return fail(err)
			}
		}
	}
	if event == nil {
		return prepared, nil
	}
	prepared.payloads = make(map[datasetGroup][]byte, len(grouped))
	for _, group := range sortedDatasetGroups(grouped) {
		payload, err := event(group.spaceID, group.datasetID, grouped[group])
		if err != nil {
			return fail(err)
		}
		eventMessage := &eventpb.EventMessage{}
		if err := proto.Unmarshal(payload, eventMessage); err != nil {
			return fail(fmt.Errorf("unmarshal rows.upserted event for %s/%s: %w", group.spaceID, group.datasetID, err))
		}
		if err := validateDatasetRowsUpsertedEvent(eventMessage, ""); err != nil {
			return fail(fmt.Errorf("validate rows.upserted event for %s/%s: %w", group.spaceID, group.datasetID, err))
		}
		if eventMessage.GetSpaceId() != group.spaceID || eventMessage.GetSubjectId() != group.datasetID {
			return fail(fmt.Errorf("rows.upserted event identity %s/%s does not match write group %s/%s", eventMessage.GetSpaceId(), eventMessage.GetSubjectId(), group.spaceID, group.datasetID))
		}
		prepared.payloads[group] = payload
	}
	return prepared, nil
}

// commitFieldsWriteLocked 在 outboxMu 内完成写入：检查数据集未删除、按数据集顺序分配 outbox 编号并绑定到事件、
// 追加变更日志与来源事件标记，然后提交。调用方必须持有 outboxMu。
func (s *Store) commitFieldsWriteLocked(prepared *preparedFieldsWrite, sourceEventID string, changeLog bool, decorate func(*cpebble.Batch, []*OutboxEntry) error) ([]*OutboxEntry, error) {
	batch, grouped := prepared.batch, prepared.grouped
	for group := range grouped {
		deleted, err := s.isDatasetDeleted(group.spaceID, group.datasetID)
		if err != nil {
			return nil, err
		}
		if deleted {
			return nil, ErrDatasetDeleted
		}
	}
	entries := make([]*OutboxEntry, 0, len(grouped))
	if prepared.payloads != nil {
		nextID, err := s.nextOutboxID()
		if err != nil {
			return nil, err
		}
		for _, group := range sortedDatasetGroups(grouped) {
			id := nextID
			payload, err := BindOutboxID(prepared.payloads[group], s.nodeID, s.sourceStoreID, id)
			if err != nil {
				return nil, err
			}
			if s.maxEventBytes > 0 && len(payload) > s.maxEventBytes {
				return nil, invalidf("event payload size %d exceeds limit %d", len(payload), s.maxEventBytes)
			}
			if err := batch.Set([]byte(outboxKey(id)), payload, s.writeOptions); err != nil {
				return nil, err
			}
			if err := s.setNextOutboxID(batch, id+1); err != nil {
				return nil, err
			}
			entries = append(entries, &OutboxEntry{ID: id, Data: append([]byte(nil), payload...), CreatedAt: time.Now().UTC()})
			if sourceEventID != "" {
				createdAt := time.Now().UTC()
				markerKey := processedSourceEventKey(sourceEventID, group)
				if err := batch.Set(markerKey, encodeProcessedEventTimestamp(createdAt), s.writeOptions); err != nil {
					return nil, err
				}
				if err := batch.Set(processedSourceEventTimeKey(createdAt, markerKey), markerKey, s.writeOptions); err != nil {
					return nil, err
				}
			}
			nextID++
		}
	}
	if changeLog {
		if err := s.stageChangeLog(batch, grouped, time.Now()); err != nil {
			return nil, err
		}
	}
	if decorate != nil {
		if err := decorate(batch, entries); err != nil {
			return nil, err
		}
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		s.noteOutboxCommitted(len(entries), entries[0].CreatedAt)
	}
	return entries, nil
}

// CleanupProcessedSourceEvents removes source-event dedupe markers older than
// the retention window. It serializes with source writes so a marker cannot be
// deleted between the duplicate check and the corresponding row mutation.
func (s *Store) CleanupProcessedSourceEvents(ctx context.Context, now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.CleanupProcessedSourceEventsBefore(ctx, now.UTC().Add(-s.processedEventRetention))
}

// CleanupProcessedSourceEventsBefore removes markers created before cutoff.
// The time index makes cleanup proportional to expired markers rather than the
// full marker set. Each bounded batch releases outboxMu before the next batch
// so maintenance cannot hold the write path hostage for an unbounded scan.
func (s *Store) CleanupProcessedSourceEventsBefore(ctx context.Context, cutoff time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("pebble store is closed")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if cutoff.IsZero() {
		return 0, errors.New("processed event cleanup cutoff is required")
	}
	cutoff = cutoff.UTC()
	removed := 0
	for {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		count, err := s.cleanupProcessedSourceEventBatch(ctx, cutoff)
		removed += count
		if err != nil || count < processedEventCleanupBatchSize {
			return removed, err
		}
	}
}

func (s *Store) cleanupProcessedSourceEventBatch(ctx context.Context, cutoff time.Time) (int, error) {
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()

	upperBound := processedSourceEventTimeKey(cutoff, nil)
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(processedEventTimePrefix), UpperBound: upperBound})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	keys := make([][]byte, 0, processedEventCleanupBatchSize)
	markers := make([][]byte, 0, processedEventCleanupBatchSize)
	for valid := iter.First(); valid && len(keys) < processedEventCleanupBatchSize; valid = iter.Next() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		keys = append(keys, append([]byte(nil), iter.Key()...))
		markers = append(markers, append([]byte(nil), iter.Value()...))
	}
	if err := iter.Error(); err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	for i, key := range keys {
		if err := batch.Delete(key, s.writeOptions); err != nil {
			return 0, err
		}
		if len(markers[i]) != 0 {
			if err := batch.Delete(markers[i], s.writeOptions); err != nil {
				return 0, err
			}
		}
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return 0, err
	}
	return len(keys), nil
}

func encodeProcessedEventTimestamp(at time.Time) []byte {
	var value [8]byte
	binary.BigEndian.PutUint64(value[:], uint64(at.UnixNano()))
	return value[:]
}

func (s *Store) hasProcessedSourceEvent(sourceEventID string, group datasetGroup) (bool, error) {
	value, closer, err := s.db.Get(processedSourceEventKey(sourceEventID, group))
	if errors.Is(err, cpebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	closer.Close()
	return len(value) > 0, nil
}

func processedSourceEventKey(sourceEventID string, group datasetGroup) []byte {
	hash := sha256.Sum256([]byte(sourceEventID + "\x00" + group.spaceID + "\x00" + group.datasetID))
	key := []byte(processedEventPrefix)
	key = appendRawPart(key, []byte(group.spaceID))
	key = appendRawPart(key, []byte(group.datasetID))
	return appendRawPart(key, []byte(hex.EncodeToString(hash[:])))
}

func processedDatasetEventPrefix(spaceID, datasetID string) []byte {
	key := []byte(processedEventPrefix)
	key = appendRawPart(key, []byte(spaceID))
	return appendRawPart(key, []byte(datasetID))
}

func processedSourceEventTimeKey(createdAt time.Time, markerKey []byte) []byte {
	key := processedEventTimePrefix + hex.EncodeToString(encodeProcessedEventTimestamp(createdAt))
	if len(markerKey) == 0 {
		return []byte(key)
	}
	return []byte(key + "/" + hex.EncodeToString(markerKey))
}

func sortedDatasetGroups(grouped map[datasetGroup][]*pb.RowFieldUpsert) []datasetGroup {
	groups := make([]datasetGroup, 0, len(grouped))
	for group := range grouped {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].spaceID == groups[j].spaceID {
			return groups[i].datasetID < groups[j].datasetID
		}
		return groups[i].spaceID < groups[j].spaceID
	})
	return groups
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

type datasetGroup struct{ spaceID, datasetID string }

func groupRowsByDataset(rows []*pb.RowFieldUpsert) map[datasetGroup][]*pb.RowFieldUpsert {
	out := make(map[datasetGroup][]*pb.RowFieldUpsert)
	for _, row := range rows {
		key := row.GetKey()
		group := datasetGroup{spaceID: key.GetSpaceId(), datasetID: key.GetDatasetId()}
		out[group] = append(out[group], row)
	}
	return out
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
