package pebble

import (
	"context"
	"errors"
	"fmt"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// 完成事件的转发：转发协程按游标跟随变更日志里的完成标记条目，把它们发到 EventBus。
// 游标是“已处理到的变更序号”，存在 metaMarkerRelayCursor 里，发布成功才推进；重启后从游标接着发，
// 至少投递一次（事件 ID 由标记内容决定，重发不变，下游按事件 ID 去重）。
const metaMarkerRelayCursor = "__meta/marker_relay_cursor"

// MarkerChange 是变更日志里的一条完成标记条目。
type MarkerChange struct {
	// Sequence 是这条标记在变更日志里的序号。
	Sequence uint64
	// CreatedAt 是标记提交的时间，读不出来时为零值。
	CreatedAt time.Time
	// Data 是整条标记消息（EventMessage）的字节。
	Data []byte
}

// MarkerPage 是一次扫描变更日志得到的完成标记。
type MarkerPage struct {
	Markers []MarkerChange
	// Scanned 是已扫描到的变更序号：Markers 全部处理完之后，游标可以推进到它。
	Scanned uint64
	// HasMore 表示扫描因为条数或字节上限停下，后面还有条目。
	HasMore bool
	// Skipped 表示游标之后的条目已被保留期清理删除，本次从最早可用的条目开始。
	Skipped bool
}

// ReadMarkerChanges 从序号 after 之后扫描最多 limit 条变更日志，返回其中的完成标记条目。
// 一页放进结果的标记累计不超过 maxChangePageBytes（单条超过时单独成页）。
func (s *Store) ReadMarkerChanges(ctx context.Context, after uint64, limit int) (MarkerPage, error) {
	if s == nil || s.db == nil {
		return MarkerPage{}, errors.New("pebble store is closed")
	}
	if limit <= 0 {
		limit = defaultChangeLimit
	}
	// 清理位置与日志条目从同一个快照读，并发清理不会让扫描漏报被删掉的条目。
	snap := s.db.NewSnapshot()
	defer snap.Close()
	trimmed, err := readUint64Meta(snap, metaChangeTrimmed)
	if err != nil {
		return MarkerPage{}, err
	}
	page := MarkerPage{}
	if after < trimmed {
		page.Skipped = true
		after = trimmed
	}
	page.Scanned = after
	iter, err := snap.NewIter(&cpebble.IterOptions{LowerBound: changeLogKey(after + 1), UpperBound: []byte(changeLogPrefix + "\xff")})
	if err != nil {
		return MarkerPage{}, err
	}
	defer iter.Close()
	scanned, pageBytes := 0, 0
	for ok := iter.First(); ok; ok = iter.Next() {
		if err := ctx.Err(); err != nil {
			return MarkerPage{}, err
		}
		if scanned >= limit {
			page.HasMore = true
			break
		}
		head, err := peekChange(iter.Value())
		if err != nil {
			return MarkerPage{}, fmt.Errorf("decode change log %s: %w", iter.Key(), err)
		}
		if scanned == 0 && head.sequence != after+1 {
			// 日志序号连续、只从头清理：第一条不是 after+1，说明中间的条目已被删除。
			page.Skipped = true
		}
		scanned++
		if head.hasMarker {
			size := len(iter.Value())
			if len(page.Markers) > 0 && pageBytes+size > maxChangePageBytes {
				page.HasMore = true
				break
			}
			change := &pb.DatasetChange{}
			if err := proto.Unmarshal(iter.Value(), change); err != nil {
				return MarkerPage{}, fmt.Errorf("decode change log %s: %w", iter.Key(), err)
			}
			var created time.Time
			if ms := change.GetCreatedAt(); ms > 0 {
				created = time.UnixMilli(ms)
			}
			page.Markers = append(page.Markers, MarkerChange{Sequence: head.sequence, CreatedAt: created, Data: change.GetMarker()})
			pageBytes += size
		}
		page.Scanned = head.sequence
	}
	if err := iter.Error(); err != nil {
		return MarkerPage{}, err
	}
	return page, nil
}

// MarkerRelayCursor 返回转发协程持久化的游标；从未记过时为 0，即从日志开头转发。
func (s *Store) MarkerRelayCursor() (uint64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("pebble store is closed")
	}
	return readUint64Meta(s.db, metaMarkerRelayCursor)
}

// SetMarkerRelayCursor 记下转发协程已处理到的变更序号。只有转发协程一个写入方。
func (s *Store) SetMarkerRelayCursor(sequence uint64) error {
	if s == nil || s.db == nil {
		return errors.New("pebble store is closed")
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := setUint64Meta(batch, metaMarkerRelayCursor, sequence, s.writeOptions); err != nil {
		return err
	}
	return batch.Commit(s.writeOptions)
}

// MarkerWake 返回完成标记的提交通知：每次提交了完成标记后收到一个信号（多次提交合并为一个）。
func (s *Store) MarkerWake() <-chan struct{} { return s.markerWake }

// MarkerPublishable 报告变更日志里的一条完成标记现在还该不该发布。
// 标记本体已经不在（数据集被删除，或标记随保留期清理）时返回 false，转发协程跳过它；
// 标记无法通过事件契约校验时返回 ErrInvalidMarker。
func (s *Store) MarkerPublishable(raw []byte) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("pebble store is closed")
	}
	message, err := validateMarkerMessage(raw)
	if err != nil {
		return false, fmt.Errorf("%w：%v", ErrInvalidMarker, err)
	}
	payload := &storageeventpb.DatasetPeriodCompleted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return false, fmt.Errorf("%w：%v", ErrInvalidMarker, err)
	}
	key := markerKey(message.GetSpaceId(), payload.GetDatasetId(), payload.GetFrequency(), payload.GetPeriodTime(), payload.GetSource(), payload.GetCauseEventId())
	existing, found, err := s.readMarkerRecord(key)
	if err != nil {
		return false, err
	}
	// 比较整条内容：删除后重建的同名数据集可以用同一个事件 ID 重报内容不同的标记，旧条目不能再发。
	return found && sameMarkerPayload(existing, message), nil
}
