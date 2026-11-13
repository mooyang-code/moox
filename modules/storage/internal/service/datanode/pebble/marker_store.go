package pebble

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// 周期完成标记：键为 __marker/{空间}/{数据集}/{频率}/{周期}/{来源}/{触发事件哈希}，前四段与账本相同；
// 账本判定的标记（source=ledger）触发事件部分为空，每期一条；写入方上报的标记（source=producer）按触发事件区分，
// 补算会让同一期有多条。标记随数据集保留期与账本一起清理。
const markerPrefix = "__marker/"

const (
	markerSourceLedger   = "ledger"
	markerSourceProducer = "producer"
)

func markerDatasetPrefix(spaceID, datasetID string) string {
	return markerPrefix + datasetScope(spaceID, datasetID)
}

// markerKeyIn 在频率一级的前缀下拼出一期某来源的标记键。
func markerKeyIn(frequencyPrefix string, periodTime int64, source, causeEventID string) []byte {
	key := frequencyPrefix + periodTimePart(periodTime) + "/" + source + "/"
	if causeEventID != "" {
		sum := sha256.Sum256([]byte(causeEventID))
		key += hex.EncodeToString(sum[:])
	}
	return []byte(key)
}

func markerKey(spaceID, datasetID, frequency string, periodTime int64, source, causeEventID string) []byte {
	return markerKeyIn(markerPrefix+snapshotScope(spaceID, datasetID, frequency), periodTime, source, causeEventID)
}

func ledgerMarkerKey(spaceID, datasetID, frequency string, periodTime int64) []byte {
	return markerKey(spaceID, datasetID, frequency, periodTime, markerSourceLedger, "")
}

// AppendDatasetMarker 追加一条写入方上报的周期完成标记：标记记录、Outbox 条目与变更日志条目同批提交。
// 标记按“触发事件 + 周期”幂等：内容相同的重报直接成功，内容不同返回 ConflictError。
func (s *Store) AppendDatasetMarker(ctx context.Context, raw []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	message, err := validateDataNodeMarkerMessage(raw)
	if err != nil {
		return "", err
	}
	payload := &storageeventpb.DatasetPeriodCompleted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		return "", fmt.Errorf("unmarshal dataset period marker: %w", err)
	}
	key := markerKey(message.GetSpaceId(), payload.GetDatasetId(), payload.GetFrequency(), payload.GetPeriodTime(), payload.GetSource(), payload.GetCauseEventId())
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	if err := s.requireDataset(message.GetSpaceId(), message.GetSubjectId()); err != nil {
		return "", err
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	if payload.GetSource() == markerSourceProducer {
		// 写入方标记的事件 ID 只由触发事件与周期决定、不含频率，所以按这两项在各频率下查重。
		existing, found, err := s.findProducerMarker(message.GetSpaceId(), payload.GetDatasetId(), payload.GetCauseEventId(), payload.GetPeriodTime())
		if err != nil {
			return "", err
		}
		if found {
			if !sameMarkerPayload(existing, message) {
				return "", ConflictError{EventID: message.GetEventId()}
			}
			return message.GetEventId(), nil
		}
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	written, err := s.stageDatasetMarkerLocked(batch, key, message, raw)
	if err != nil {
		return "", err
	}
	if !written {
		return message.GetEventId(), nil
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return "", err
	}
	s.noteMarkerCommitted(time.Now().UTC())
	return message.GetEventId(), nil
}

// stageDatasetMarkerLocked 是两个标记写入点（写入方上报的 AppendDatasetMarker、账本判定的 finalizePeriodLocked）
// 共用的写入：把标记记录、一条 Outbox 条目和一条变更日志的完成标记条目（内嵌整条标记消息）放进调用方的 batch。
// key 上已有内容相同的标记时什么都不写、返回 written=false；内容不同返回 ConflictError。
// 它从数据库（不是 batch）读下一个 Outbox 编号与变更序号，所以传入的 batch 里不能已有 Outbox 或变更日志条目，否则编号会撞车。
// 调用方持有 outboxMu，负责提交 batch，并在提交成功且 written 时调 noteMarkerCommitted。
func (s *Store) stageDatasetMarkerLocked(batch *cpebble.Batch, key []byte, message *eventpb.EventMessage, raw []byte) (bool, error) {
	existing, found, err := s.readMarkerRecord(key)
	if err != nil {
		return false, err
	}
	if found {
		if !sameMarkerPayload(existing, message) {
			return false, ConflictError{EventID: message.GetEventId()}
		}
		return false, nil
	}
	if s.maxEventBytes > 0 && len(raw) > s.maxEventBytes {
		return false, invalidf("event payload size %d exceeds limit %d", len(raw), s.maxEventBytes)
	}
	nextID, err := s.nextOutboxID()
	if err != nil {
		return false, err
	}
	if err := batch.Set(key, raw, s.writeOptions); err != nil {
		return false, err
	}
	if err := batch.Set([]byte(outboxKey(nextID)), raw, s.writeOptions); err != nil {
		return false, err
	}
	if err := s.setNextOutboxID(batch, nextID+1); err != nil {
		return false, err
	}
	if err := s.stageChangeLocked(batch, &pb.DatasetChange{SpaceId: message.GetSpaceId(), DatasetId: message.GetSubjectId(), Marker: raw}); err != nil {
		return false, err
	}
	return true, nil
}

// noteMarkerCommitted 在含标记的 batch 提交成功后更新 Outbox 提示，并唤醒等待新变更的读取。
func (s *Store) noteMarkerCommitted(at time.Time) {
	s.noteOutboxCommitted(1, at)
	s.noteChangeCommitted()
}

// GetDatasetPeriodCompletedMarker 按“触发事件 + 周期”查询写入方上报的周期完成。
func (s *Store) GetDatasetPeriodCompletedMarker(ctx context.Context, spaceID, datasetID, causeEventID string, periodTime int64) (*eventpb.EventMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return s.findProducerMarker(spaceID, datasetID, causeEventID, periodTime)
}

// findProducerMarker 在数据集下每个频率点读一次“周期/producer/触发事件哈希”。一个数据集通常只有一个频率。
func (s *Store) findProducerMarker(spaceID, datasetID, causeEventID string, periodTime int64) (*eventpb.EventMessage, bool, error) {
	var found *eventpb.EventMessage
	errFound := errors.New("found")
	err := s.eachFrequencyPrefix(markerDatasetPrefix(spaceID, datasetID), func(frequencyPrefix string) error {
		message, ok, err := s.readMarkerRecord(markerKeyIn(frequencyPrefix, periodTime, markerSourceProducer, causeEventID))
		if err != nil {
			return err
		}
		if ok {
			found = message
			return errFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, errFound) {
		return nil, false, err
	}
	return found, found != nil, nil
}

func (s *Store) readMarkerRecord(key []byte) (*eventpb.EventMessage, bool, error) {
	value, closer, err := s.db.Get(key)
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	raw := append([]byte(nil), value...)
	if err := closer.Close(); err != nil {
		return nil, false, err
	}
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, message); err != nil {
		return nil, false, fmt.Errorf("unmarshal persisted dataset marker: %w", err)
	}
	return message, true, nil
}

func sameMarkerPayload(left, right *eventpb.EventMessage) bool {
	return left != nil && right != nil &&
		left.GetEventId() == right.GetEventId() && left.GetEventName() == right.GetEventName() && left.GetEventVersion() == right.GetEventVersion() &&
		left.GetSpaceId() == right.GetSpaceId() && left.GetSubjectId() == right.GetSubjectId() && bytes.Equal(left.GetPayload(), right.GetPayload())
}
