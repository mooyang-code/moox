package view

import (
	"context"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

type appliedKey struct {
	indexID string
	nodeID  string
	storeID string
}

type pendingViewReady struct {
	spaceID  string
	viewID   string
	required []*storagepb.CommittedPosition
	payload  *storagepb.ViewDataReady
	opts     events.PublishOptions
}

func (s *Service) NoteAppliedPosition(spaceID, viewID, indexID, nodeID, storeID string, sequence uint64) {
	if s == nil || strings.TrimSpace(spaceID) == "" || strings.TrimSpace(viewID) == "" || strings.TrimSpace(indexID) == "" || strings.TrimSpace(nodeID) == "" || strings.TrimSpace(storeID) == "" || sequence == 0 {
		return
	}
	key := appliedFenceKey{spaceID: spaceID, viewID: viewID, indexID: indexID, nodeID: nodeID, storeID: storeID}
	s.appliedFenceMu.Lock()
	if s.appliedFence == nil {
		s.appliedFence = make(map[appliedFenceKey]uint64)
	}
	if sequence > s.appliedFence[key] {
		s.appliedFence[key] = sequence
	}
	s.appliedFenceMu.Unlock()
	_ = s.persistAppliedFence()
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.applied == nil {
		runtime.applied = make(map[appliedKey]uint64)
	}
	applied := appliedKey{indexID: indexID, nodeID: nodeID, storeID: storeID}
	if current := runtime.applied[applied]; sequence > current {
		runtime.applied[applied] = sequence
	}
}

func (s *Service) FlushViewDataReady(ctx context.Context, spaceID, viewID string) error {
	if s == nil {
		return nil
	}
	publisher := s.readyPublisherLocked()
	if publisher == nil {
		return nil
	}
	s.pendingReadyMu.Lock()
	pending := append([]pendingViewReady(nil), s.pendingReady...)
	s.pendingReady = s.pendingReady[:0]
	s.pendingReadyMu.Unlock()
	remaining := make([]pendingViewReady, 0, len(pending))
	for _, item := range pending {
		if spaceID != "" && (item.spaceID != spaceID || (viewID != "" && item.viewID != viewID)) {
			remaining = append(remaining, item)
			continue
		}
		if item.payload != nil && writeFenceRequiresPositions(item.payload.GetCompletionKind()) && !hasUsableCommittedPositions(item.required) {
			continue
		}
		view := s.viewSnapshot(item.spaceID, item.viewID)
		if view == nil || !s.positionsApplied(view, item.required) {
			remaining = append(remaining, item)
			continue
		}
		if _, err := publisher.Publish(ctx, events.ViewDataReady, item.payload, item.opts); err != nil {
			if s.metrics != nil {
				s.metrics.ObserveReadyPublishRetry(item.viewID, "view_data_ready")
			}
			remaining = append(remaining, item)
			s.restorePending(remaining)
			if persistErr := s.persistPendingReady(); persistErr != nil {
				return persistErr
			}
			return err
		}
	}
	s.restorePending(remaining)
	return s.persistPendingReady()
}

func (s *Service) enqueueViewDataReady(view *pb.View, required []*storagepb.CommittedPosition, payload *storagepb.ViewDataReady, message *eventpb.EventMessage, eventID string) {
	if view == nil || payload == nil || message == nil || strings.TrimSpace(eventID) == "" {
		return
	}
	if writeFenceRequiresPositions(payload.GetCompletionKind()) && !hasUsableCommittedPositions(required) {
		return
	}
	occurredAt := message.GetOccurredAt()
	if payload.GetReadyAt() != nil {
		occurredAt = payload.GetReadyAt()
	}
	item := pendingViewReady{
		spaceID: view.GetSpaceId(), viewID: view.GetViewId(), required: cloneCommittedPositions(required),
		payload: payload,
		opts: events.PublishOptions{
			EventID: eventID, OccurredAt: occurredAt.AsTime().UTC(), SpaceID: view.GetSpaceId(), SubjectID: view.GetViewId(),
		},
	}
	s.pendingReadyMu.Lock()
	defer s.pendingReadyMu.Unlock()
	for _, existing := range s.pendingReady {
		if existing.opts.EventID == eventID {
			return
		}
	}
	s.pendingReady = append(s.pendingReady, item)
	_ = s.persistPendingReadyLocked()
}

func (s *Service) restorePending(items []pendingViewReady) {
	if len(items) == 0 {
		return
	}
	s.pendingReadyMu.Lock()
	defer s.pendingReadyMu.Unlock()
	for _, item := range items {
		dup := false
		for _, existing := range s.pendingReady {
			if existing.opts.EventID == item.opts.EventID {
				dup = true
				break
			}
		}
		if !dup {
			s.pendingReady = append(s.pendingReady, item)
		}
	}
	_ = s.persistPendingReadyLocked()
}

func (s *Service) persistPendingReady() error {
	if s == nil {
		return nil
	}
	s.pendingReadyMu.Lock()
	defer s.pendingReadyMu.Unlock()
	return s.persistPendingReadyLocked()
}

func (s *Service) positionsApplied(view *pb.View, required []*storagepb.CommittedPosition) bool {
	if view == nil {
		return false
	}
	if len(required) == 0 {
		return true
	}
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}]
	s.mu.RUnlock()
	indexID := view.GetActiveIndexId()
	if runtime != nil {
		runtime.mu.Lock()
		if runtime.active != "" {
			indexID = runtime.active
		}
		runtime.mu.Unlock()
	}
	if indexID == "" {
		return false
	}
	for _, position := range required {
		if position == nil || position.GetNodeId() == "" || position.GetStoreId() == "" || position.GetSequence() == 0 {
			return false
		}
		if s.appliedSequence(view.GetSpaceId(), view.GetViewId(), indexID, position.GetNodeId(), position.GetStoreId()) < position.GetSequence() {
			return false
		}
	}
	return true
}

func (s *Service) noteAppliedFromPayload(spaceID string, payload *storagepb.DatasetRowsUpserted) {
	if payload == nil || payload.GetSourceSequence() == 0 || payload.GetSourceNodeId() == "" || payload.GetSourceStoreId() == "" {
		return
	}
	views := s.viewsForDataset(spaceID, payload.GetDatasetId(), true)
	for _, view := range views {
		s.mu.RLock()
		runtime := s.views[viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}]
		s.mu.RUnlock()
		if runtime == nil {
			continue
		}
		runtime.mu.Lock()
		activeID, nextID := runtime.active, runtime.next
		runtime.mu.Unlock()
		if activeID != "" {
			s.NoteAppliedPosition(view.GetSpaceId(), view.GetViewId(), activeID, payload.GetSourceNodeId(), payload.GetSourceStoreId(), payload.GetSourceSequence())
		}
		if nextID != "" {
			s.NoteAppliedPosition(view.GetSpaceId(), view.GetViewId(), nextID, payload.GetSourceNodeId(), payload.GetSourceStoreId(), payload.GetSourceSequence())
		}
	}
}

func (s *Service) viewSnapshot(spaceID, viewID string) *pb.View {
	s.mu.RLock()
	view := s.catalogViews[viewRef{spaceID: spaceID, viewID: viewID}]
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if view == nil {
		return nil
	}
	clone := proto.Clone(view).(*pb.View)
	if runtime != nil {
		runtime.mu.Lock()
		if runtime.active != "" {
			clone.ActiveIndexId = runtime.active
		}
		runtime.mu.Unlock()
	}
	return clone
}

func (s *Service) readyPublisherLocked() ReadyEventPublisher {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readyPublisher
}

func writeFenceRequiresPositions(kind string) bool {
	return kind == events.FactorPeriodComputed.Name() || kind == events.MergePeriodCompleted.Name()
}

func hasUsableCommittedPositions(required []*storagepb.CommittedPosition) bool {
	for _, position := range required {
		if position != nil && strings.TrimSpace(position.GetNodeId()) != "" && strings.TrimSpace(position.GetStoreId()) != "" && position.GetSequence() != 0 {
			return true
		}
	}
	return false
}

func viewVisibleScope(view *pb.View, fallback string) string {
	if view != nil && strings.TrimSpace(view.GetFilterJson()) != "" {
		return "view:" + view.GetViewId()
	}
	return firstNonEmpty(fallback, "view:"+view.GetViewId())
}
