package pebble

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// 行变更日志：每次写入时间序列行，在同一个 Pebble batch 里追加一条日志记录（数据集、标的、受影响的时间范围）。
// 与 outbox 不同，日志不随发布而删除，保留一段时间（默认 7 天）供因子机的本地副本补读：
// 本地靠它得知哪些区间被写入或修正，离线或通知丢失都不影响正确性；读到断档时由对账命令重建基线。
const (
	changeLogPrefix    = "__changelog/"
	metaNextChangeSeq  = "__meta/next_change_seq"
	metaChangeTrimmed  = "__meta/changelog_trimmed_through"
	defaultChangeLimit = 500
	maxChangeLimit     = 2000
	changeCleanupBatch = 512

	// DefaultChangeLogRetention 是变更日志的默认保留期。
	DefaultChangeLogRetention = 7 * 24 * time.Hour
)

func changeLogKey(seq uint64) []byte { return []byte(fmt.Sprintf("%s%020d", changeLogPrefix, seq)) }

// ChangeLogRetention 返回变更日志的保留期。
func (s *Store) ChangeLogRetention() time.Duration {
	if s == nil || s.changeLogRetention <= 0 {
		return DefaultChangeLogRetention
	}
	return s.changeLogRetention
}

func (s *Store) readUint64Meta(key string) (uint64, error) {
	data, closer, err := s.db.Get([]byte(key))
	if errors.Is(err, cpebble.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	if len(data) != 8 {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return binary.BigEndian.Uint64(data), nil
}

func setUint64Meta(batch *cpebble.Batch, key string, value uint64, opts *cpebble.WriteOptions) error {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	return batch.Set([]byte(key), data[:], opts)
}

// nextChangeSeq 返回下一个变更序号（从 1 开始）。调用方必须持有 outboxMu，所有写入都在它的保护下提交。
func (s *Store) nextChangeSeq() (uint64, error) {
	next, err := s.readUint64Meta(metaNextChangeSeq)
	if err != nil {
		return 0, err
	}
	if next == 0 {
		next = 1
	}
	return next, nil
}

// stageChangeLog 把一次写入的变更记录追加进 batch。ranges 由 changeRanges 在锁外算好（只含时间序列行，
// 同一个标的在一次写入里的多行合并成一个时间范围）；没有时间序列行时不记。调用方必须持有 outboxMu。
func (s *Store) stageChangeLog(batch *cpebble.Batch, spaceID, datasetID string, ranges []*pb.DatasetChangeRange, now time.Time) error {
	if len(ranges) == 0 {
		return nil
	}
	seq, err := s.nextChangeSeq()
	if err != nil {
		return err
	}
	raw, err := proto.Marshal(&pb.DatasetChange{
		Sequence: seq, SpaceId: spaceID, DatasetId: datasetID, Ranges: ranges,
		CreatedAt: now.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	if err := batch.Set(changeLogKey(seq), raw, s.writeOptions); err != nil {
		return err
	}
	return setUint64Meta(batch, metaNextChangeSeq, seq+1, s.writeOptions)
}

// changeRanges 按 (标的, 频率) 合并一次写入里时间序列行的 data_time 范围。
func changeRanges(rows []*pb.RowFieldUpsert) []*pb.DatasetChangeRange {
	type key struct{ subject, freq string }
	type span struct{ lo, hi string }
	spans := map[key]*span{}
	for _, row := range rows {
		ts := row.GetKey().GetTimeSeries()
		if ts == nil {
			continue
		}
		// 行键已经规范化成固定宽度的 UTC 时间串，字符串比较与时间比较一致。
		k := key{subject: ts.GetSubjectId(), freq: ts.GetFreq()}
		at := ts.GetDataTime()
		if cur := spans[k]; cur == nil {
			spans[k] = &span{lo: at, hi: at}
		} else {
			if at < cur.lo {
				cur.lo = at
			}
			if at > cur.hi {
				cur.hi = at
			}
		}
	}
	keys := make([]key, 0, len(spans))
	for k := range spans {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].subject != keys[j].subject {
			return keys[i].subject < keys[j].subject
		}
		return keys[i].freq < keys[j].freq
	})
	out := make([]*pb.DatasetChangeRange, 0, len(keys))
	for _, k := range keys {
		sp := spans[k]
		out = append(out, &pb.DatasetChangeRange{SubjectId: k.subject, Freq: k.freq, StartTime: canonicalToRFC3339(sp.lo), EndTime: canonicalToRFC3339(sp.hi)})
	}
	return out
}

func canonicalToRFC3339(value string) string {
	if at, err := time.Parse(canonicalTimeLayout, value); err == nil {
		return at.UTC().Format(time.RFC3339)
	}
	return value
}

// ChangeCursor 返回变更日志头部的位点：此刻之前提交的变更序号都不大于它。
func (s *Store) ChangeCursor() (*pb.DatasetChangeCursor, error) {
	next, err := s.nextChangeSeq()
	if err != nil {
		return nil, err
	}
	return &pb.DatasetChangeCursor{NodeId: s.nodeID, StoreId: s.sourceStoreID, Sequence: next - 1}, nil
}

// ReadDatasetChanges 从游标之后按序读取变更日志。游标早于保留起点、比头部还新、或身份与本存储对不上时返回 gap。
func (s *Store) ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq) (*pb.ReadDatasetChangesRsp, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return nil, invalid("space_id and dataset_id are required")
	}
	head, err := s.ChangeCursor()
	if err != nil {
		return nil, err
	}
	rsp := &pb.ReadDatasetChangesRsp{NextCursor: head}
	if req.GetFromHead() {
		return rsp, nil
	}
	cursor := req.GetCursor()
	if cursor == nil {
		return nil, invalid("cursor is required unless from_head is set")
	}
	trimmed, err := s.readUint64Meta(metaChangeTrimmed)
	if err != nil {
		return nil, err
	}
	if cursor.GetNodeId() != s.nodeID || cursor.GetStoreId() != s.sourceStoreID ||
		cursor.GetSequence() > head.GetSequence() || cursor.GetSequence() < trimmed {
		rsp.Gap = true
		return rsp, nil
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultChangeLimit
	}
	limit = min(limit, maxChangeLimit)
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: changeLogKey(cursor.GetSequence() + 1), UpperBound: []byte(changeLogPrefix + "\xff")})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	last := cursor.GetSequence()
	scanned := 0
	for ok := iter.First(); ok; ok = iter.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if scanned >= limit {
			rsp.HasMore = true
			break
		}
		scanned++
		var change pb.DatasetChange
		if err := proto.Unmarshal(iter.Value(), &change); err != nil {
			return nil, fmt.Errorf("decode change log %s: %w", iter.Key(), err)
		}
		last = change.GetSequence()
		if change.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if change.GetDatasetId() != req.GetDatasetId() {
			continue
		}
		rsp.Changes = append(rsp.Changes, &change)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	rsp.NextCursor = &pb.DatasetChangeCursor{NodeId: s.nodeID, StoreId: s.sourceStoreID, Sequence: last}
	return rsp, nil
}

// CleanupChangeLog 删除创建时间早于 cutoff 的变更日志，并记下被清理到的序号（游标早于它就是断档）。
func (s *Store) CleanupChangeLog(ctx context.Context, cutoff time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("pebble store is closed")
	}
	total := 0
	for {
		n, err := s.cleanupChangeLogBatch(ctx, cutoff)
		total += n
		if err != nil || n < changeCleanupBatch {
			return total, err
		}
	}
}

func (s *Store) cleanupChangeLogBatch(ctx context.Context, cutoff time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// 与写入共用 outboxMu：删除和追加互不穿插，序号元数据保持一致。
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(changeLogPrefix), UpperBound: []byte(changeLogPrefix + "\xff")})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	batch := s.db.NewBatch()
	defer batch.Close()
	count := 0
	var lastSeq uint64
	for ok := iter.First(); ok && count < changeCleanupBatch; ok = iter.Next() {
		var change pb.DatasetChange
		if err := proto.Unmarshal(iter.Value(), &change); err != nil {
			return 0, fmt.Errorf("decode change log %s: %w", iter.Key(), err)
		}
		created, err := time.Parse(time.RFC3339Nano, change.GetCreatedAt())
		if err != nil || !created.Before(cutoff) {
			break
		}
		if err := batch.Delete(append([]byte(nil), iter.Key()...), s.writeOptions); err != nil {
			return 0, err
		}
		lastSeq = change.GetSequence()
		count++
	}
	if count == 0 {
		return 0, nil
	}
	if err := setUint64Meta(batch, metaChangeTrimmed, lastSeq, s.writeOptions); err != nil {
		return 0, err
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return 0, err
	}
	return count, nil
}
