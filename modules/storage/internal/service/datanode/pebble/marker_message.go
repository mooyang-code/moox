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

func BuildCollectorPeriodCompletedMessage(spaceID string, marker *pb.CollectorPeriodCompletedMarker) ([]byte, string, error) {
	if marker == nil {
		return nil, "", invalid("collector period marker is required")
	}
	payload := &storageeventpb.CollectorPeriodCompleted{
		DatasetId: marker.GetDatasetId(), Frequency: marker.GetFrequency(), PeriodTime: marker.GetPeriodTime(),
		Status: marker.GetStatus(), BatchId: marker.GetBatchId(), ConfigSnapshotId: marker.GetConfigSnapshotId(),
		ExpectedScopeRef: marker.GetExpectedScopeRef(), ExpectedSubjectIds: normalizedIDs(marker.GetExpectedSubjectIds()),
		FailedSubjects:     normalizedIDs(marker.GetFailedSubjects()),
		CommittedPositions: eventPositions(marker.GetCommittedPositions()),
		CollectedAt:        marker.GetCollectedAt(),
	}
	encodedPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal collector period marker payload: %w", err)
	}
	payloadHash := sha256.Sum256(encodedPayload)
	eventID := stableMarkerID("collector-period", spaceID, payload.GetDatasetId(), payload.GetFrequency(), strconv.FormatInt(payload.GetPeriodTime(), 10), hex.EncodeToString(payloadHash[:16]))
	return buildMarkerMessage(events.CollectorPeriodCompleted, eventID, spaceID, payload.GetDatasetId(), markerTime(payload.GetCollectedAt()), payload)
}

func BuildMergePeriodCompletedMessage(spaceID string, marker *pb.MergePeriodCompletedMarker) ([]byte, string, error) {
	if marker == nil {
		return nil, "", invalid("merge period marker is required")
	}
	payload := &storageeventpb.MergePeriodCompleted{
		DatasetId: marker.GetDatasetId(), Frequency: marker.GetFrequency(), PeriodTime: marker.GetPeriodTime(),
		Status: marker.GetStatus(), BatchId: marker.GetBatchId(), ConfigSnapshotId: marker.GetConfigSnapshotId(),
		ExpectedScopeRef: marker.GetExpectedScopeRef(), ExpectedSubjectIds: normalizedIDs(marker.GetExpectedSubjectIds()),
		FailedSubjects:     normalizedIDs(marker.GetFailedSubjects()),
		CommittedPositions: eventPositions(marker.GetCommittedPositions()),
		CompletedAt:        marker.GetCompletedAt(),
	}
	encodedPayload, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal merge period marker payload: %w", err)
	}
	payloadHash := sha256.Sum256(encodedPayload)
	eventID := stableMarkerID("merge-period", spaceID, payload.GetDatasetId(), payload.GetFrequency(), strconv.FormatInt(payload.GetPeriodTime(), 10), hex.EncodeToString(payloadHash[:16]))
	return buildMarkerMessage(events.MergePeriodCompleted, eventID, spaceID, payload.GetDatasetId(), markerTime(payload.GetCompletedAt()), payload)
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
		DatasetId: marker.GetDatasetId(), Frequency: marker.GetFrequency(), PeriodTime: marker.GetPeriodTime(),
		Status: marker.GetStatus(), BatchId: marker.GetBatchId(), ConfigSnapshotId: marker.GetConfigSnapshotId(),
		ExpectedScopeRef: marker.GetExpectedScopeRef(), ExpectedSubjectIds: normalizedIDs(marker.GetExpectedSubjectIds()),
		Bindings: bindings, CommittedPositions: eventPositions(marker.GetCommittedPositions()),
		ComputedAt: marker.GetComputedAt(), TriggerEventId: marker.GetTriggerEventId(),
	}
	eventID := stableMarkerID("factor-period", spaceID, payload.GetDatasetId(), payload.GetTriggerEventId(), strconv.FormatInt(payload.GetPeriodTime(), 10))
	return buildMarkerMessage(events.FactorPeriodComputed, eventID, spaceID, payload.GetDatasetId(), markerTime(payload.GetComputedAt()), payload)
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

func eventPositions(values []*pb.CommittedPosition) []*storageeventpb.CommittedPosition {
	out := make([]*storageeventpb.CommittedPosition, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		out = append(out, &storageeventpb.CommittedPosition{
			NodeId: value.GetNodeId(), StoreId: value.GetStoreId(), Sequence: value.GetSequence(),
		})
	}
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
	for _, allowed := range []events.Event{
		events.DatasetRowsUpserted,
		events.CollectorPeriodCompleted,
		events.MergePeriodCompleted,
		events.FactorPeriodComputed,
		events.DatasetSyncPoint,
	} {
		if event.Name() == allowed.Name() && event.Version() == allowed.Version() {
			return true
		}
	}
	return false
}
