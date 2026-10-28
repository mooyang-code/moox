package pebble

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"sort"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// commitMarkerPrefix 下按数据集存放 commit 标记：键为 __commit/<space>/<dataset>/<sha256(commit_id)>，值为内容摘要。
// 标记永久有效，只随删除数据集清理：若按时间过期，同一 commit_id 的旧内容重放会覆盖期间补算写入的新结果。
const commitMarkerPrefix = "__commit/"

// writeLogThreshold 是写入分段耗时日志的阈值。
const writeLogThreshold = 50 * time.Millisecond

// UpsertFields 写入一个数据集的行：字段、属性、行索引、一条行变更事件与变更日志在同一个 Pebble 批次里提交。
//
// commitID 可选。非空时同一数据集内同一 commitID 只生效一次：内容相同返回成功（不再写入，返回的条目为空），
// 内容不同返回 CommitConflictError。
func (s *Store) UpsertFields(ctx context.Context, spaceID, datasetID, commitID string, rows []*pb.RowFieldUpsert) ([]*OutboxEntry, error) {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" {
		return nil, invalid("space_id and dataset_id are required")
	}
	if commitID != "" {
		if err := validateCommitID(commitID); err != nil {
			return nil, err
		}
	}
	// 分段计时：规整、准备（字段、行索引、事件与变更范围）、等写锁、提交；达到阈值时记一行。
	started := time.Now()
	var normalizedAt, preparedAt, lockedAt, committedAt time.Time
	defer func() {
		if committedAt.IsZero() || time.Since(started) < writeLogThreshold {
			return
		}
		log.Printf("storage node 写入 dataset=%s 行=%d commit=%t 规整=%s 准备=%s 等锁=%s 提交=%s 合计=%s",
			datasetID, len(rows), commitID != "", normalizedAt.Sub(started).Round(time.Millisecond), preparedAt.Sub(normalizedAt).Round(time.Millisecond),
			lockedAt.Sub(preparedAt).Round(time.Millisecond), committedAt.Sub(lockedAt).Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
	}()
	normalized, err := s.normalizeWriteRows(ctx, rows)
	if err != nil {
		return nil, err
	}
	for _, row := range normalized {
		if row.GetKey().GetSpaceId() != spaceID || row.GetKey().GetDatasetId() != datasetID {
			return nil, invalid("row dataset identity does not match request")
		}
	}
	var digest []byte
	if commitID != "" {
		if digest, err = commitDigest(spaceID, datasetID, normalized); err != nil {
			return nil, err
		}
	}
	normalizedAt = time.Now()

	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	// 编码 KV、构造并校验事件、整理变更范围与锁无关，放在 outboxMu 之外：并发写入、完成标记与 outbox 转发
	// 不必排在这一步后面。锁内只查 commit 标记并提交；重复提交白做一次准备，丢弃即可。
	write, err := s.prepareRowsWrite(ctx, spaceID, datasetID, normalized)
	if err != nil {
		return nil, err
	}
	defer write.batch.Close()
	preparedAt = time.Now()
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	lockedAt = time.Now()
	var markerKey []byte
	if commitID != "" {
		markerKey = commitMarkerKey(spaceID, datasetID, commitID)
		existing, found, err := s.readCommitMarker(markerKey)
		if err != nil {
			return nil, err
		}
		if found {
			if !bytes.Equal(existing, digest) {
				return nil, CommitConflictError{CommitID: commitID}
			}
			committedAt = time.Now()
			return nil, nil
		}
	}
	entries, err := s.commitRowsWriteLocked(ctx, write, func(batch *cpebble.Batch) error {
		if markerKey == nil {
			return nil
		}
		return batch.Set(markerKey, digest, s.writeOptions)
	})
	if err != nil {
		return nil, err
	}
	committedAt = time.Now()
	return entries, nil
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

// preparedRowsWrite 是一次写入在锁外准备好的部分：已写入字段、属性与行索引 KV 的 batch，构造并校验过、
// 还没绑定 outbox 编号的事件，以及变更日志的时间范围。
type preparedRowsWrite struct {
	batch     *cpebble.Batch
	spaceID   string
	datasetID string
	payload   []byte
	ranges    []*pb.DatasetChangeRange
}

// prepareRowsWrite 只读 Store 的静态配置，不读写 outbox、变更序号与 commit 标记，可以在 outboxMu 之外并发执行。
func (s *Store) prepareRowsWrite(ctx context.Context, spaceID, datasetID string, rows []*pb.RowFieldUpsert) (*preparedRowsWrite, error) {
	// One batch preserves all field changes from a request. The dataset event
	// payload, change log and commit marker are staged into the same batch.
	batch := s.db.NewBatch()
	fail := func(err error) (*preparedRowsWrite, error) {
		_ = batch.Close()
		return nil, err
	}
	for _, row := range rows {
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
				return fail(err)
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
				return fail(err)
			}
			if err := batch.Set(key, data, s.writeOptions); err != nil {
				return fail(err)
			}
		}
	}
	payload, err := BuildDatasetRowsUpsertedMessage(spaceID, datasetID, rows)
	if err != nil {
		return fail(err)
	}
	return &preparedRowsWrite{batch: batch, spaceID: spaceID, datasetID: datasetID, payload: payload, ranges: changeRanges(rows)}, nil
}

// commitRowsWriteLocked 在 outboxMu 内完成写入：检查数据集未删除、分配 outbox 编号并绑定到事件、追加变更日志，
// 由 decorate 补充同一批次里的其他键（commit 标记、周期位图），然后提交。调用方必须持有 outboxMu。
func (s *Store) commitRowsWriteLocked(ctx context.Context, prepared *preparedRowsWrite, decorate func(*cpebble.Batch) error) ([]*OutboxEntry, error) {
	// 等锁期间请求可能已超时或取消：此时不再提交，避免客户端重试后写出重复事件。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deleted, err := s.isDatasetDeleted(prepared.spaceID, prepared.datasetID)
	if err != nil {
		return nil, err
	}
	if deleted {
		return nil, ErrDatasetDeleted
	}
	batch := prepared.batch
	id, err := s.nextOutboxID()
	if err != nil {
		return nil, err
	}
	payload, err := BindOutboxID(prepared.payload, s.nodeID, s.sourceStoreID, id)
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
	if err := s.stageChangeLog(batch, prepared.spaceID, prepared.datasetID, prepared.ranges, time.Now()); err != nil {
		return nil, err
	}
	if decorate != nil {
		if err := decorate(batch); err != nil {
			return nil, err
		}
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return nil, err
	}
	entry := &OutboxEntry{ID: id, Data: payload, CreatedAt: time.Now().UTC()}
	s.noteOutboxCommitted(1, entry.CreatedAt)
	return []*OutboxEntry{entry}, nil
}

// commitDigest 是一次提交内容的摘要：行键不得重复，行与字段按规范顺序排列，与请求里的顺序无关。
func commitDigest(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
	keys := make([][]byte, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for i, row := range rows {
		identity, err := proto.MarshalOptions{Deterministic: true}.Marshal(row.GetKey())
		if err != nil {
			return nil, err
		}
		if _, exists := seen[string(identity)]; exists {
			return nil, invalid("duplicate row key in commit")
		}
		seen[string(identity)] = struct{}{}
		keys[i] = identity
	}
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return bytes.Compare(keys[order[a]], keys[order[b]]) < 0 })
	canonical := make([]*pb.RowFieldUpsert, len(rows))
	for i, index := range order {
		row := proto.Clone(rows[index]).(*pb.RowFieldUpsert)
		sort.Slice(row.Fields, func(a, b int) bool { return row.Fields[a].GetFieldId() < row.Fields[b].GetFieldId() })
		canonical[i] = row
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(&pb.RowsUpserted{SpaceId: spaceID, DatasetId: datasetID, Rows: canonical})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func validateCommitID(commitID string) error {
	if strings.TrimSpace(commitID) == "" || strings.TrimSpace(commitID) != commitID || strings.ContainsAny(commitID, "\x00") {
		return invalid("commit_id is invalid")
	}
	return nil
}

func datasetCommitPrefix(spaceID, datasetID string) []byte {
	key := []byte(commitMarkerPrefix)
	key = appendRawPart(key, []byte(spaceID))
	return appendRawPart(key, []byte(datasetID))
}

func commitMarkerKey(spaceID, datasetID, commitID string) []byte {
	sum := sha256.Sum256([]byte(commitID))
	return appendRawPart(datasetCommitPrefix(spaceID, datasetID), sum[:])
}

func (s *Store) readCommitMarker(key []byte) ([]byte, bool, error) {
	value, closer, err := s.db.Get(key)
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), true, nil
}
