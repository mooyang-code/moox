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
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

const (
	markerRecordPrefix = "__dataset_marker/"
	factorMarkerPrefix = "__factor_marker/"
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
	if message.GetEventName() == events.FactorPeriodComputed.Name() {
		indexKey, err := factorMarkerIndexKey(message)
		if err != nil {
			return "", err
		}
		if err := batch.Set(indexKey, recordKey, s.writeOptions); err != nil {
			return "", err
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

func (s *Store) GetFactorPeriodComputedMarker(ctx context.Context, spaceID, resultDatasetID, sourceViewID, triggerEventID string, periodTime int64) (*eventpb.EventMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	key := factorMarkerLookupKey(spaceID, resultDatasetID, sourceViewID, triggerEventID, periodTime)
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

func factorMarkerIndexKey(message *eventpb.EventMessage) ([]byte, error) {
	registry, err := eventsRegistry()
	if err != nil {
		return nil, err
	}
	_, payload, err := decodeRawMarker(registry, message)
	if err != nil {
		return nil, err
	}
	return factorMarkerLookupKey(message.GetSpaceId(), payload.resultDatasetID, payload.sourceViewID, payload.triggerEventID, payload.periodTime), nil
}

func factorMarkerLookupKey(spaceID, resultDatasetID, sourceViewID, triggerEventID string, periodTime int64) []byte {
	hash := sha256.Sum256([]byte(strings.Join([]string{spaceID, resultDatasetID, sourceViewID, triggerEventID, strconv.FormatInt(periodTime, 10)}, "\x00")))
	return []byte(factorMarkerPrefix + hex.EncodeToString(hash[:]))
}

type factorMarkerIdentity struct {
	resultDatasetID string
	sourceViewID    string
	triggerEventID  string
	periodTime      int64
}

func eventsRegistry() (*events.Registry, error) { return events.DefaultRegistry() }

func decodeRawMarker(registry *events.Registry, message *eventpb.EventMessage) (*eventpb.EventMessage, factorMarkerIdentity, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		return nil, factorMarkerIdentity{}, err
	}
	subject, err := registry.RenderSubject(events.FactorPeriodComputed, message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return nil, factorMarkerIdentity{}, err
	}
	envelope, value, err := events.DecodeRaw(registry, raw, subject, message.GetEventId(), events.ContentType)
	if err != nil {
		return nil, factorMarkerIdentity{}, err
	}
	payload, ok := value.(*storageeventpb.FactorPeriodComputed)
	if !ok {
		return nil, factorMarkerIdentity{}, fmt.Errorf("unexpected factor marker payload %T", value)
	}
	return envelope, factorMarkerIdentity{
		resultDatasetID: payload.GetResultDatasetId(), sourceViewID: payload.GetSourceViewId(), triggerEventID: payload.GetTriggerEventId(), periodTime: payload.GetPeriodTime(),
	}, nil
}
