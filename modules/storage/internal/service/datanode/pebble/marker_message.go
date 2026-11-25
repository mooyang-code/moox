package pebble

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// LedgerPeriodCompleted 是序列账本判定出的周期完成结果（source=ledger），由 storage-primary 自己在周期封口时生成。
type LedgerPeriodCompleted struct {
	DatasetID          string
	Frequency          string
	PeriodTime         int64
	Status             string
	SnapshotRef        string
	UniverseSubjectIDs []string
	FailedSubjects     []string
	// CompletedAt 是判定完成的时刻（Unix 毫秒）。
	CompletedAt int64
}

// BuildLedgerPeriodCompletedMessage 把账本的判定结果构造成 DatasetPeriodCompleted（source=ledger）事件。
func BuildLedgerPeriodCompletedMessage(spaceID string, completed *LedgerPeriodCompleted) ([]byte, string, error) {
	if completed == nil {
		return nil, "", invalid("ledger period completion is required")
	}
	payload := &storageeventpb.DatasetPeriodCompleted{
		DatasetId: completed.DatasetID, Frequency: completed.Frequency, PeriodTime: completed.PeriodTime,
		Status: completed.Status, Source: "ledger", SnapshotRef: completed.SnapshotRef,
		UniverseSubjectIds: normalizedIDs(completed.UniverseSubjectIDs),
		FailedSubjects:     normalizedIDs(completed.FailedSubjects),
		CompletedAt:        completed.CompletedAt,
	}
	encodedPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal ledger period completion payload: %w", err)
	}
	payloadHash := sha256.Sum256(encodedPayload)
	eventID := stableMarkerID("period-completed", "ledger", spaceID, payload.GetDatasetId(), payload.GetFrequency(), strconv.FormatInt(payload.GetPeriodTime(), 10), hex.EncodeToString(payloadHash[:16]))
	return buildMarkerMessage(events.DatasetPeriodCompleted, eventID, spaceID, payload.GetDatasetId(), time.UnixMilli(payload.GetCompletedAt()), payload)
}

// BuildProducerPeriodCompletedMessage 把写入方报告的周期完成构造成 DatasetPeriodCompleted（source=producer）事件；
// 明细（detail）是不透明字节，原样带入事件。
func BuildProducerPeriodCompletedMessage(spaceID string, marker *pb.DatasetPeriodCompletedMarker) ([]byte, string, error) {
	if marker == nil {
		return nil, "", invalid("dataset period marker is required")
	}
	payload := &storageeventpb.DatasetPeriodCompleted{
		DatasetId: marker.GetDatasetId(), Frequency: marker.GetFrequency(), PeriodTime: marker.GetPeriodTime(),
		Status: marker.GetStatus(), Source: "producer",
		UniverseSubjectIds: append([]string(nil), marker.GetUniverseSubjectIds()...),
		FailedSubjects:     append([]string(nil), marker.GetFailedSubjects()...),
		CauseEventId:       marker.GetCauseEventId(), CompletedAt: marker.GetCompletedAt(),
		DetailType: marker.GetDetailType(), Detail: append([]byte(nil), marker.GetDetail()...),
	}
	eventID := stableMarkerID("producer-period", spaceID, payload.GetDatasetId(), payload.GetCauseEventId(), strconv.FormatInt(payload.GetPeriodTime(), 10))
	return buildMarkerMessage(events.DatasetPeriodCompleted, eventID, spaceID, payload.GetDatasetId(), time.UnixMilli(payload.GetCompletedAt()), payload)
}

func buildMarkerMessage(event events.Event, eventID, spaceID, subjectID string, occurredAt time.Time, payload proto.Message) ([]byte, string, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, "", err
	}
	encoded, err := registry.Encode(event, payload, events.PublishOptions{
		EventID: eventID, OccurredAt: occurredAt.UTC(), SpaceID: spaceID, SubjectID: subjectID,
	})
	if err != nil {
		// 编码时的事件契约校验（全集必填、series_tag 非空等）不通过是请求本身的问题，重试也不会通过：标成无效请求。
		return nil, "", invalidf("完成标记无法通过事件校验：%v", err)
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(encoded.Message)
	return raw, eventID, err
}

func stableMarkerID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "storage-marker-" + hex.EncodeToString(hash[:16])
}

func normalizedIDs(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			unique[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(unique))
	for value := range unique {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func validateMarkerMessage(raw []byte) (*eventpb.EventMessage, error) {
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, message); err != nil {
		return nil, fmt.Errorf("unmarshal dataset marker: %w", err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	event, ok := registry.Lookup(message.GetEventName(), message.GetEventVersion())
	if !ok || !isMarkerEvent(event) {
		return nil, fmt.Errorf("事件 %s@%d 不是周期完成标记", message.GetEventName(), message.GetEventVersion())
	}
	subject, err := registry.RenderSubject(event, message.GetSpaceId(), message.GetSubjectId())
	if err != nil {
		return nil, err
	}
	if _, _, err := events.DecodeRaw(registry, raw, subject, message.GetEventId(), events.ContentType); err != nil {
		return nil, fmt.Errorf("validate dataset marker: %w", err)
	}
	return message, nil
}

func isMarkerEvent(event events.Event) bool {
	return event.Name() == events.DatasetPeriodCompleted.Name() && event.Version() == events.DatasetPeriodCompleted.Version()
}
