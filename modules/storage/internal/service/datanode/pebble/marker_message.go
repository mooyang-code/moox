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
	"google.golang.org/protobuf/types/known/timestamppb"
)

func BuildDatasetPeriodCollectedMessage(spaceID string, marker *pb.DatasetPeriodCollectedMarker) ([]byte, string, error) {
	if marker == nil {
		return nil, "", invalid("dataset period marker is required")
	}
	payload := &storageeventpb.DatasetPeriodCollected{
		DatasetId: marker.GetDatasetId(), Frequency: marker.GetFrequency(), PeriodTime: marker.GetPeriodTime(),
		Status: marker.GetStatus(), SubjectIds: normalizedIDs(marker.GetSubjectIds()), FailedSubjects: normalizedIDs(marker.GetFailedSubjects()),
		CollectedAt: marker.GetCollectedAt(),
	}
	eventID := stableMarkerID("dataset-period", spaceID, payload.GetDatasetId(), payload.GetFrequency(), strconv.FormatInt(payload.GetPeriodTime(), 10))
	return buildMarkerMessage(events.DatasetPeriodCollected, eventID, spaceID, payload.GetDatasetId(), markerTime(payload.GetCollectedAt()), payload)
}

func BuildFactorPeriodComputedMessage(spaceID string, marker *pb.FactorPeriodComputedMarker) ([]byte, string, error) {
	if marker == nil {
		return nil, "", invalid("factor period marker is required")
	}
	bindings := make([]*storageeventpb.FactorBindingPeriodState, 0, len(marker.GetBindings()))
	for _, state := range marker.GetBindings() {
		if state == nil {
			continue
		}
		bindings = append(bindings, &storageeventpb.FactorBindingPeriodState{
			BindingId: state.GetBindingId(), FactorId: state.GetFactorId(), Status: state.GetStatus(),
			SkippedSubjects: normalizedIDs(state.GetSkippedSubjects()), FailedSubjects: normalizedIDs(state.GetFailedSubjects()),
			SourceHash: state.GetSourceHash(),
		})
	}
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].GetBindingId() != bindings[j].GetBindingId() {
			return bindings[i].GetBindingId() < bindings[j].GetBindingId()
		}
		return bindings[i].GetFactorId() < bindings[j].GetFactorId()
	})
	payload := &storageeventpb.FactorPeriodComputed{
		SourceViewId: marker.GetSourceViewId(), ResultDatasetId: marker.GetResultDatasetId(), Frequency: marker.GetFrequency(),
		PeriodTime: marker.GetPeriodTime(), Status: marker.GetStatus(), Bindings: bindings,
		ComputedAt: marker.GetComputedAt(), TriggerEventId: marker.GetTriggerEventId(), SourceIndexId: marker.GetSourceIndexId(), SourceIndexRevision: marker.GetSourceIndexRevision(),
	}
	eventID := stableMarkerID("factor-period", spaceID, payload.GetResultDatasetId(), payload.GetTriggerEventId(), strconv.FormatInt(payload.GetPeriodTime(), 10))
	return buildMarkerMessage(events.FactorPeriodComputed, eventID, spaceID, payload.GetResultDatasetId(), markerTime(payload.GetComputedAt()), payload)
}

func BuildDatasetSyncPointMessage(spaceID string, marker *pb.DatasetSyncPointMarker) ([]byte, string, error) {
	if marker == nil {
		return nil, "", invalid("dataset sync point is required")
	}
	syncPointID := stableMarkerID("sync-point", marker.GetRequestId(), marker.GetDatasetId())
	if marker.GetSyncPointId() != "" && marker.GetSyncPointId() != syncPointID {
		return nil, "", invalid("sync_point_id does not match request_id and dataset_id")
	}
	payload := &storageeventpb.DatasetSyncPoint{
		SyncPointId: syncPointID, RequestId: marker.GetRequestId(), DatasetId: marker.GetDatasetId(), Source: marker.GetSource(),
	}
	return buildMarkerMessage(events.DatasetSyncPoint, syncPointID, spaceID, payload.GetDatasetId(), time.Now().UTC(), payload)
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
		return nil, "", err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(encoded.Message)
	return raw, eventID, err
}

func markerTime(value *timestamppb.Timestamp) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.AsTime().UTC()
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

func validateDataNodeMarkerMessage(raw []byte) (*eventpb.EventMessage, error) {
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, message); err != nil {
		return nil, fmt.Errorf("unmarshal dataset marker: %w", err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	event, ok := registry.Lookup(message.GetEventName(), message.GetEventVersion())
	if !ok || !isDataNodeOutboxEvent(event) {
		return nil, invalidf("unsupported DataNode outbox event %s@%d", message.GetEventName(), message.GetEventVersion())
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

func isDataNodeOutboxEvent(event events.Event) bool {
	for _, allowed := range []events.Event{events.DatasetRowsUpserted, events.DatasetPeriodCollected, events.FactorPeriodComputed, events.DatasetSyncPoint} {
		if event.Name() == allowed.Name() && event.Version() == allowed.Version() {
			return true
		}
	}
	return false
}
