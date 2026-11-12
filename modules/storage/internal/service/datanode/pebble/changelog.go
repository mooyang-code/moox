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

// 变更日志：每次行写入、每条周期完成标记，都在同一个 Pebble batch 里追加一条日志记录，占一个全局递增的序号。
//   - 行条目：时间序列行记标的与受影响的时间范围，记录型行记被写入的记录键。
//   - 完成标记条目：内嵌整条标记消息的字节（写入方上报与账本判定两个写入点都记；同步点不记）。
//
// 与 outbox 不同，日志不随发布而删除，保留一段时间（默认 7 天），由保留期清理循环删除：
// 因子机的本地副本靠它得知哪些区间被写入或修正，View 靠它跟随写入与完成标记；读到断档时由读取方重建基线。
const (
	changeLogPrefix    = "__changelog/"
	metaNextChangeSeq  = "__meta/next_change_seq"
	metaChangeTrimmed  = "__meta/changelog_trimmed_through"
	defaultChangeLimit = 500
	maxChangeLimit     = 2000
	maxChangeWait      = 30 * time.Second
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

// stageChangeLocked 给一条变更分配序号与创建时间，追加进 batch。序号从数据库（不是 batch）读，
// 所以一个 batch 只能追加一条变更：传入的 batch 里不能已有变更日志条目，否则序号会撞车。调用方必须持有 outboxMu，
// 并在提交成功后调 noteChangeCommitted 唤醒等待新变更的读取。
func (s *Store) stageChangeLocked(batch *cpebble.Batch, change *pb.DatasetChange) error {
	seq, err := s.nextChangeSeq()
	if err != nil {
		return err
	}
	change.Sequence = seq
	// 创建时间用墙钟：清理按墙钟算截止时间，不能用周期判定的时钟。
	change.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := proto.Marshal(change)
	if err != nil {
		return err
	}
	if err := batch.Set(changeLogKey(seq), raw, s.writeOptions); err != nil {
		return err
	}
	return setUint64Meta(batch, metaNextChangeSeq, seq+1, s.writeOptions)
}

// stageRowsChangeLocked 追加一次行写入的变更条目；ranges 与 records 都为空时不记。调用方必须持有 outboxMu。
func (s *Store) stageRowsChangeLocked(batch *cpebble.Batch, spaceID, datasetID string, ranges []*pb.DatasetChangeRange, records []*pb.RecordRowKey) error {
	if len(ranges) == 0 && len(records) == 0 {
		return nil
	}
	return s.stageChangeLocked(batch, &pb.DatasetChange{SpaceId: spaceID, DatasetId: datasetID, Ranges: ranges, Records: records})
}

// noteChangeCommitted 在含变更条目的 batch 提交成功后广播一次：关掉当前的信号通道、换一个新的，
// 唤醒所有在 ReadDatasetChanges 里等待新变更的读取。
func (s *Store) noteChangeCommitted() {
	s.changeSignalMu.Lock()
	close(s.changeSignal)
	s.changeSignal = make(chan struct{})
	s.changeSignalMu.Unlock()
}

// changeCommitted 返回当前的信号通道：下一次变更提交时它被关闭。
func (s *Store) changeCommitted() <-chan struct{} {
	s.changeSignalMu.Lock()
	defer s.changeSignalMu.Unlock()
	return s.changeSignal
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
		out = append(out, &pb.DatasetChangeRange{SubjectId: k.subject, Freq: k.freq, StartTime: canonicalToRFC3339Nano(sp.lo), EndTime: canonicalToRFC3339Nano(sp.hi)})
	}
	return out
}

// changeRecords 收集一次写入里记录型行的记录键，去重后按 (record_id, version) 排序。
func changeRecords(rows []*pb.RowFieldUpsert) []*pb.RecordRowKey {
	type key struct{ id, version string }
	seen := map[key]struct{}{}
	var out []*pb.RecordRowKey
	for _, row := range rows {
		record := row.GetKey().GetRecord()
		if record == nil {
			continue
		}
		k := key{id: record.GetRecordId(), version: record.GetVersion()}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, &pb.RecordRowKey{RecordId: k.id, Version: k.version})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetRecordId() != out[j].GetRecordId() {
			return out[i].GetRecordId() < out[j].GetRecordId()
		}
		return out[i].GetVersion() < out[j].GetVersion()
	})
	return out
}

func canonicalToRFC3339Nano(value string) string {
	if at, err := time.Parse(canonicalTimeLayout, value); err == nil {
		return at.UTC().Format(time.RFC3339Nano)
	}
	return value
}

// ChangeCursor 返回变更日志头部的位点：此刻之前提交的变更序号都不大于它。
func (s *Store) ChangeCursor() (*pb.DatasetChangeCursor, error) {
	next, err := s.nextChangeSeq()
	if err != nil {
		return nil, err
	}
	return &pb.DatasetChangeCursor{StoreId: s.sourceStoreID, Sequence: next - 1}, nil
}

// ReadDatasetChanges 从游标之后按序读取一个数据集的变更。游标早于保留起点、比头部还新、或 store_id 与本存储对不上时返回 gap。
// wait_ms 大于 0 时，扫描到头仍没有本数据集的变更，就等下一次变更提交后从已扫描的位置接着扫，直到有变更或到期；
// 返回的游标始终是已扫描到的位置。
func (s *Store) ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq) (*pb.ReadDatasetChangesRsp, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return nil, invalid("space_id and dataset_id are required")
	}
	if req.GetFromHead() {
		head, err := s.ChangeCursor()
		if err != nil {
			return nil, err
		}
		return &pb.ReadDatasetChangesRsp{NextCursor: head}, nil
	}
	cursor := req.GetCursor()
	if cursor == nil {
		return nil, invalid("cursor is required unless from_head is set")
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultChangeLimit
	}
	limit = min(limit, maxChangeLimit)
	wait := min(time.Duration(req.GetWaitMs())*time.Millisecond, maxChangeWait)
	var timer <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		timer = t.C
	}
	for {
		// 先拿信号通道再扫描：扫描期间提交的变更会关掉这个通道，不会漏掉唤醒。
		signal := s.changeCommitted()
		rsp, err := s.scanDatasetChanges(ctx, req.GetSpaceId(), req.GetDatasetId(), cursor, limit)
		if err != nil || wait <= 0 || rsp.GetGap() || rsp.GetHasMore() || len(rsp.GetChanges()) > 0 {
			return rsp, err
		}
		cursor = rsp.GetNextCursor()
		select {
		case <-signal:
		case <-timer:
			return rsp, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.backgroundCtx.Done():
			return rsp, nil
		}
	}
}

// scanDatasetChanges 从游标之后扫描最多 limit 条日志，返回其中属于这个数据集的条目与已扫描到的位置。
func (s *Store) scanDatasetChanges(ctx context.Context, spaceID, datasetID string, cursor *pb.DatasetChangeCursor, limit int) (*pb.ReadDatasetChangesRsp, error) {
	head, err := s.ChangeCursor()
	if err != nil {
		return nil, err
	}
	rsp := &pb.ReadDatasetChangesRsp{NextCursor: head}
	trimmed, err := s.readUint64Meta(metaChangeTrimmed)
	if err != nil {
		return nil, err
	}
	if cursor.GetStoreId() != s.sourceStoreID || cursor.GetSequence() > head.GetSequence() || cursor.GetSequence() < trimmed {
		rsp.Gap = true
		return rsp, nil
	}
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
		if change.GetSpaceId() != spaceID || change.GetDatasetId() != datasetID {
			continue
		}
		rsp.Changes = append(rsp.Changes, &change)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	rsp.NextCursor = &pb.DatasetChangeCursor{StoreId: s.sourceStoreID, Sequence: last}
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
