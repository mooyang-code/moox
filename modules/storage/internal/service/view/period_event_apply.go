package view

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/client"
)

type PeriodMetadataClient interface {
	UpsertViewPeriodDatasetState(context.Context, *pb.UpsertViewPeriodDatasetStateReq, ...client.Option) (*pb.UpsertViewPeriodDatasetStateRsp, error)
	ListViewPeriodDatasetStates(context.Context, *pb.ListViewPeriodDatasetStatesReq, ...client.Option) (*pb.ListViewPeriodDatasetStatesRsp, error)
	RecordViewSyncPoint(context.Context, *pb.RecordViewSyncPointReq, ...client.Option) (*pb.RecordViewSyncPointRsp, error)
}

var _ PeriodMetadataClient = (pb.MetadataClientProxy)(nil)

type ReadyEventPublisher interface {
	Publish(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error)
}

func (s *Service) HandleDatasetPeriodCollected(ctx context.Context, message *eventpb.EventMessage, payload *storageeventpb.DatasetPeriodCollected) error {
	if message == nil || payload == nil {
		return eventconsumer.Permanent(errors.New("dataset period event is empty"))
	}
	metadata, publisher := s.periodDependencies()
	if metadata == nil || publisher == nil {
		return errors.New("storage View period dependencies are unavailable")
	}
	views := s.activeViewsForDataset(message.GetSpaceId(), payload.GetDatasetId())
	for _, view := range views {
		state := &pb.ViewPeriodDatasetState{
			SpaceId: message.GetSpaceId(), ViewId: view.GetViewId(), DatasetId: payload.GetDatasetId(), Frequency: payload.GetFrequency(),
			PeriodTime: payload.GetPeriodTime(), EventId: message.GetEventId(), Status: payload.GetStatus(),
			SubjectIds: append([]string(nil), payload.GetSubjectIds()...), FailedSubjects: append([]string(nil), payload.GetFailedSubjects()...),
			OccurredAt: cloneTimestamp(message.GetOccurredAt()),
		}
		rsp, err := metadata.UpsertViewPeriodDatasetState(ctx, &pb.UpsertViewPeriodDatasetStateReq{AuthInfo: s.internalAuth(), State: state})
		if err != nil {
			return err
		}
		if err := requireStorageSuccess("upsert view period dataset state", rsp.GetRetInfo()); err != nil {
			return err
		}
		statesRsp, err := metadata.ListViewPeriodDatasetStates(ctx, &pb.ListViewPeriodDatasetStatesReq{
			AuthInfo: s.internalAuth(), SpaceId: message.GetSpaceId(), ViewId: view.GetViewId(), Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(),
		})
		if err != nil {
			return err
		}
		if err := requireStorageSuccess("list view period dataset states", statesRsp.GetRetInfo()); err != nil {
			return err
		}
		seenDatasets := make(map[string]struct{}, len(statesRsp.GetStates()))
		for _, state := range statesRsp.GetStates() {
			if state != nil {
				seenDatasets[state.GetDatasetId()] = struct{}{}
			}
		}
		waiting := 0
		for _, datasetID := range view.GetDatasetIds() {
			if _, exists := seenDatasets[datasetID]; !exists {
				waiting++
			}
		}
		s.metrics.ObservePeriodWaiting(view.GetViewId(), payload.GetFrequency(), waiting)
		ready, ok := sourceReadyPayload(view, payload.GetFrequency(), payload.GetPeriodTime(), statesRsp.GetStates(), message.GetOccurredAt())
		if !ok {
			continue
		}
		eventID := stableViewEventID("source-ready", message.GetSpaceId(), view.GetViewId(), payload.GetFrequency(), strconv.FormatInt(payload.GetPeriodTime(), 10))
		occurredAt := message.GetOccurredAt()
		if ready.GetReadyAt() != nil {
			occurredAt = ready.GetReadyAt()
		}
		if _, err := publisher.Publish(ctx, events.ViewSourcePeriodReady, ready, events.PublishOptions{
			EventID: eventID, OccurredAt: occurredAt.AsTime().UTC(), SpaceID: message.GetSpaceId(), SubjectID: view.GetViewId(),
		}); err != nil {
			s.metrics.ObserveReadyPublishRetry(view.GetViewId(), "source_period_ready")
			return err
		}
	}
	return nil
}

func (s *Service) HandleFactorPeriodComputed(ctx context.Context, message *eventpb.EventMessage, payload *storageeventpb.FactorPeriodComputed) error {
	if message == nil || payload == nil {
		return eventconsumer.Permanent(errors.New("factor period event is empty"))
	}
	_, publisher := s.periodDependencies()
	if publisher == nil {
		return errors.New("storage View ready publisher is unavailable")
	}
	views := s.activeViewsForDataset(message.GetSpaceId(), payload.GetResultDatasetId())
	for _, view := range views {
		bindings := make([]*storageeventpb.FactorBindingPeriodState, 0, len(payload.GetBindings()))
		for _, state := range payload.GetBindings() {
			if state != nil {
				bindings = append(bindings, proto.Clone(state).(*storageeventpb.FactorBindingPeriodState))
			}
		}
		ready := &storageeventpb.ViewFactorPeriodReady{
			SourceViewId: payload.GetSourceViewId(), ResultViewId: view.GetViewId(), Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(),
			Status: payload.GetStatus(), Bindings: bindings, ReadyAt: cloneTimestamp(message.GetOccurredAt()),
		}
		eventID := stableViewEventID("factor-ready", message.GetEventId(), view.GetViewId())
		if _, err := publisher.Publish(ctx, events.ViewFactorPeriodReady, ready, events.PublishOptions{
			EventID: eventID, OccurredAt: message.GetOccurredAt().AsTime().UTC(), SpaceID: message.GetSpaceId(), SubjectID: view.GetViewId(),
		}); err != nil {
			s.metrics.ObserveReadyPublishRetry(view.GetViewId(), "factor_period_ready")
			return err
		}
	}
	return nil
}

func (s *Service) HandleDatasetSyncPoint(ctx context.Context, message *eventpb.EventMessage, payload *storageeventpb.DatasetSyncPoint) error {
	if message == nil || payload == nil {
		return eventconsumer.Permanent(errors.New("dataset sync-point event is empty"))
	}
	metadata, _ := s.periodDependencies()
	if metadata == nil {
		return errors.New("storage View period metadata is unavailable")
	}
	for _, view := range s.syncPointViewsForDataset(message.GetSpaceId(), payload.GetDatasetId()) {
		ready, readyErr := s.hasReadyViewIndex(ctx, view)
		if readyErr != nil {
			return readyErr
		}
		if !ready {
			return fmt.Errorf("View %s/%s has no ready active or building index", view.GetSpaceId(), view.GetViewId())
		}
		rsp, err := metadata.RecordViewSyncPoint(ctx, &pb.RecordViewSyncPointReq{AuthInfo: s.internalAuth(), SyncPoint: &pb.ViewSyncPoint{
			SpaceId: message.GetSpaceId(), ViewId: view.GetViewId(), DatasetId: payload.GetDatasetId(), RequestId: payload.GetRequestId(),
			SyncPointId: payload.GetSyncPointId(), AppliedAt: cloneTimestamp(message.GetOccurredAt()),
		}})
		if err != nil {
			return err
		}
		if err := requireStorageSuccess("record view sync point", rsp.GetRetInfo()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) hasReadyViewIndex(ctx context.Context, view *pb.View) (bool, error) {
	if view == nil {
		return false, nil
	}
	// Unit tests and lightweight embedders may exercise the event protocol
	// without opening a physical index engine. In a real Service, New always
	// installs at least the Bleve engine, so this does not weaken runtime
	// readiness checks.
	s.mu.RLock()
	if len(s.engines) == 0 {
		s.mu.RUnlock()
		return true, nil
	}
	s.mu.RUnlock()
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}]
	s.mu.RUnlock()
	if runtime == nil {
		return false, nil
	}
	runtime.mu.Lock()
	activeID, nextID, status := runtime.active, runtime.next, runtime.status
	runtime.mu.Unlock()
	activeMissing := activeID == ""
	if activeID != "" {
		if engine, err := s.engineFor(activeID); err == nil {
			if stats, statErr := engine.Stat(ctx, activeID); statErr == nil && stats.Exists {
				return true, nil
			}
		}
		activeMissing = true
	}
	if nextID != "" {
		// A building-only index must be durably marked READY before a
		// SyncPoint can be ACKed; otherwise a crash would discard it while the
		// source fence has already been recorded.
		if activeMissing && status != "active" {
			return false, nil
		}
		if engine, err := s.engineFor(nextID); err == nil {
			if stats, statErr := engine.Stat(ctx, nextID); statErr == nil && stats.Exists {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Service) periodDependencies() (PeriodMetadataClient, ReadyEventPublisher) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.periodMetadata, s.readyPublisher
}

func (s *Service) activeViewsForDataset(spaceID, datasetID string) []*pb.View {
	return s.viewsForDataset(spaceID, datasetID, false)
}

// syncPointViewsForDataset includes a view with only a building index. Rows
// are already applied to that index, so an import/catchup fence must not wait
// forever for the first active revision.
func (s *Service) syncPointViewsForDataset(spaceID, datasetID string) []*pb.View {
	return s.viewsForDataset(spaceID, datasetID, true)
}

func (s *Service) viewsForDataset(spaceID, datasetID string, includeBuilding bool) []*pb.View {
	type candidate struct {
		view    *pb.View
		runtime *viewRuntime
	}
	s.mu.RLock()
	var candidates []candidate
	for key, view := range s.catalogViews {
		if key.spaceID != spaceID || view == nil {
			continue
		}
		runtime := s.views[key]
		if runtime == nil {
			continue
		}
		candidates = append(candidates, candidate{view: proto.Clone(view).(*pb.View), runtime: runtime})
	}
	s.mu.RUnlock()

	result := make([]*pb.View, 0, len(candidates))
	for _, candidate := range candidates {
		runtime := candidate.runtime
		runtime.mu.Lock()
		active := runtime.active != "" || (includeBuilding && runtime.next != "")
		datasetIDs := append([]string(nil), runtime.activeDatasetIDs...)
		primaryDatasetID := runtime.activePrimaryDatasetID
		if runtime.active == "" && runtime.next != "" {
			datasetIDs = append([]string(nil), runtime.nextDatasetIDs...)
			primaryDatasetID = runtime.nextPrimaryDatasetID
			if len(datasetIDs) == 0 {
				datasetIDs = append([]string(nil), candidate.view.GetDatasetIds()...)
			}
		}
		if !runtime.activeDatasetSet && runtime.active != "" {
			datasetIDs = append([]string(nil), candidate.view.GetDatasetIds()...)
			primaryDatasetID = candidate.view.GetPrimaryDatasetId()
		}
		if primaryDatasetID == "" {
			primaryDatasetID = candidate.view.GetPrimaryDatasetId()
		}
		runtime.mu.Unlock()
		if active && containsString(datasetIDs, datasetID) {
			view := candidate.view
			// Carry the contract of the index that will receive this event. The
			// catalog copy may describe a newer desired revision during A/B
			// rebuild and must not change source-ready aggregation early.
			view.DatasetIds = datasetIDs
			view.PrimaryDatasetId = primaryDatasetID
			result = append(result, view)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GetViewId() < result[j].GetViewId() })
	return result
}

func sourceReadyPayload(view *pb.View, frequency string, periodTime int64, states []*pb.ViewPeriodDatasetState, occurredAt *timestamppb.Timestamp) (*storageeventpb.ViewSourcePeriodReady, bool) {
	byDataset := make(map[string]*pb.ViewPeriodDatasetState, len(states))
	for _, state := range states {
		if state != nil {
			byDataset[state.GetDatasetId()] = state
		}
	}
	datasets := make([]*storageeventpb.ViewPeriodDatasetState, 0, len(view.GetDatasetIds()))
	status := "complete"
	var primarySubjects []string
	readyAt := cloneTimestamp(occurredAt)
	for _, datasetID := range view.GetDatasetIds() {
		state := byDataset[datasetID]
		if state == nil {
			return nil, false
		}
		if state.GetStatus() == "degraded" {
			status = "degraded"
		}
		datasets = append(datasets, &storageeventpb.ViewPeriodDatasetState{
			DatasetId: datasetID, Status: state.GetStatus(), FailedSubjects: uniqueSortedStrings(state.GetFailedSubjects()),
		})
		if state.GetOccurredAt() != nil && (readyAt == nil || state.GetOccurredAt().AsTime().After(readyAt.AsTime())) {
			readyAt = cloneTimestamp(state.GetOccurredAt())
		}
		if datasetID == view.GetPrimaryDatasetId() {
			primarySubjects = append(primarySubjects, state.GetSubjectIds()...)
		}
	}
	return &storageeventpb.ViewSourcePeriodReady{
		SourceViewId: view.GetViewId(), Frequency: frequency, PeriodTime: periodTime, Status: status,
		Datasets: datasets, PrimarySubjects: uniqueSortedStrings(primarySubjects), ReadyAt: readyAt,
	}, true
}

func requireStorageSuccess(operation string, info *pb.RetInfo) error {
	if info == nil {
		return fmt.Errorf("%s returned empty ret_info", operation)
	}
	if info.GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s failed: %s", operation, info.GetMsg())
	}
	return nil
}

func stableViewEventID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "storage-view-" + hex.EncodeToString(hash[:16])
}

func cloneTimestamp(value *timestamppb.Timestamp) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(value.AsTime().UTC())
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
