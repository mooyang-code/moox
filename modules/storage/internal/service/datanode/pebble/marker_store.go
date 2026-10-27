package pebble

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/proto"
)

const (
	markerRecordPrefix = "__dataset_marker/"
	periodMarkerPrefix = "__period_marker/"
)

// AppendDatasetMarker atomically persists a governed marker and its outbox
// record. A retry with the same Event ID is a no-op when the business payload
// is identical; changing the payload is a conflict.
func (s *Store) AppendDatasetMarker(ctx context.Context, raw []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	message, err := validateDataNodeMarkerMessage(raw)
	if err != nil {
		return "", err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	deleted, err := s.isDatasetDeleted(message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return "", err
	}
	if deleted {
		return "", ErrDatasetDeleted
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()

	recordKey := datasetMarkerRecordKey(message.GetEventId())
	existing, found, err := s.readMarkerRecord(recordKey)
	if err != nil {
		return "", err
	}
	if found {
		if !sameMarkerPayload(existing, message) {
			return "", ConflictError{EventID: message.GetEventId()}
		}
		return message.GetEventId(), nil
	}
	if s.maxEventBytes > 0 && len(raw) > s.maxEventBytes {
		return "", invalidf("event payload size %d exceeds limit %d", len(raw), s.maxEventBytes)
	}
	nextID, err := s.nextOutboxID()
	if err != nil {
		return "", err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(recordKey, raw, s.writeOptions); err != nil {
		return "", err
	}
	if message.GetEventName() == events.DatasetPeriodCompleted.Name() {
		// 写入方报告的完成（producer）按 触发事件 + 周期 建索引，供写入方做幂等查询；账本判定的完成不需要。
		indexKey, indexed, err := producerMarkerIndexKey(message)
		if err != nil {
			return "", err
		}
		if indexed {
			if err := batch.Set(indexKey, recordKey, s.writeOptions); err != nil {
				return "", err
			}
		}
	}
	if err := batch.Set([]byte(outboxKey(nextID)), raw, s.writeOptions); err != nil {
		return "", err
	}
	if err := s.setNextOutboxID(batch, nextID+1); err != nil {
		return "", err
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return "", err
	}
	s.noteOutboxCommitted(1, time.Now().UTC())
	return message.GetEventId(), nil
}

// GetDatasetPeriodCompletedMarker 按 触发事件 + 周期 查询写入方报告的周期完成。
func (s *Store) GetDatasetPeriodCompletedMarker(ctx context.Context, spaceID, datasetID, causeEventID string, periodTime int64) (*eventpb.EventMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	key := periodMarkerLookupKey(spaceID, datasetID, causeEventID, periodTime)
	recordKey, closer, err := s.db.Get(key)
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	markerKey := append([]byte(nil), recordKey...)
	if err := closer.Close(); err != nil {
		return nil, false, err
	}
	raw, found, err := s.readMarkerRecord(markerKey)
	if err != nil || !found {
		return nil, false, err
	}
	return raw, true, nil
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

func datasetMarkerRecordKey(eventID string) []byte {
	hash := sha256.Sum256([]byte(eventID))
	return []byte(markerRecordPrefix + hex.EncodeToString(hash[:]))
}

// producerMarkerIndexKey 返回 producer 来源的周期完成的查询索引键；其他来源（ledger）返回 indexed=false。
func producerMarkerIndexKey(message *eventpb.EventMessage) (key []byte, indexed bool, err error) {
	registry, err := eventsRegistry()
	if err != nil {
		return nil, false, err
	}
	subject, err := registry.RenderSubject(events.DatasetPeriodCompleted, message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return nil, false, err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		return nil, false, err
	}
	_, payload, err := events.DecodeDatasetPeriodCompleted(registry, raw, subject, message.GetEventId())
	if err != nil {
		return nil, false, err
	}
	if payload.GetSource() != "producer" {
		return nil, false, nil
	}
	return periodMarkerLookupKey(message.GetSpaceId(), payload.GetDatasetId(), payload.GetCauseEventId(), payload.GetPeriodTime()), true, nil
}

func periodMarkerLookupKey(spaceID, datasetID, causeEventID string, periodTime int64) []byte {
	hash := sha256.Sum256([]byte(strings.Join([]string{spaceID, datasetID, causeEventID, strconv.FormatInt(periodTime, 10)}, "\x00")))
	return []byte(periodMarkerPrefix + hex.EncodeToString(hash[:]))
}

func eventsRegistry() (*events.Registry, error) { return events.DefaultRegistry() }
