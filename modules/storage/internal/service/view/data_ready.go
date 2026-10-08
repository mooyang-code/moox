package view

import (
	"context"
	"log"
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

// FlushViewDataReady 按入队顺序发布已满足写入围栏的就绪事件。刷新串行执行：并发刷新会互相越过，
// 打乱同一 View 的周期顺序（下游会把较早的周期记为乱序）。发布失败时，失败项与其后尚未尝试的项按原顺序
// 放回队首；无法通过契约校验的事件永远发不出去，隔离并记日志，不能堵住其后所有 View 的就绪事件。
func (s *Service) FlushViewDataReady(ctx context.Context, spaceID, viewID string) error {
	if s == nil {
		return nil
	}
	publisher := s.readyPublisherLocked()
	if publisher == nil {
		return nil
	}
	s.readyFlushMu.Lock()
	defer s.readyFlushMu.Unlock()
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	s.pendingReadyMu.Lock()
	pending := append([]pendingViewReady(nil), s.pendingReady...)
	s.pendingReady = s.pendingReady[:0]
	s.pendingReadyMu.Unlock()
	remaining := make([]pendingViewReady, 0, len(pending))
	for index, item := range pending {
		if spaceID != "" && (item.spaceID != spaceID || (viewID != "" && item.viewID != viewID)) {
			remaining = append(remaining, item)
			continue
		}
		view := s.viewSnapshot(item.spaceID, item.viewID)
		if view == nil || !s.positionsApplied(view, item.required) {
			remaining = append(remaining, item)
			continue
		}
		if _, err := registry.Encode(events.ViewDataReady, item.payload, item.opts); err != nil {
			log.Printf("View 就绪队列隔离无法通过契约校验的事件 %s（%s/%s）：%v", item.opts.EventID, item.spaceID, item.viewID, err)
			continue
		}
		if _, err := publisher.Publish(ctx, events.ViewDataReady, item.payload, item.opts); err != nil {
			if s.metrics != nil {
				s.metrics.ObserveReadyPublishRetry(item.viewID, "view_data_ready")
			}
			remaining = append(remaining, pending[index:]...)
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

// restorePending 把未发布的事件按原顺序放回队首，刷新期间新入队的事件排在它们之后。
func (s *Service) restorePending(items []pendingViewReady) {
	if len(items) == 0 {
		return
	}
	s.pendingReadyMu.Lock()
	defer s.pendingReadyMu.Unlock()
	merged := make([]pendingViewReady, 0, len(items)+len(s.pendingReady))
	seen := make(map[string]struct{}, len(items)+len(s.pendingReady))
	for _, group := range [][]pendingViewReady{items, s.pendingReady} {
		for _, item := range group {
			if _, dup := seen[item.opts.EventID]; dup {
				continue
			}
			seen[item.opts.EventID] = struct{}{}
			merged = append(merged, item)
		}
	}
	s.pendingReady = merged
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

func viewVisibleScope(view *pb.View, fallback string) string {
	if view.GetFreq() != "" {
		return "view:" + view.GetViewId()
	}
	return firstNonEmpty(fallback, "view:"+view.GetViewId())
}
