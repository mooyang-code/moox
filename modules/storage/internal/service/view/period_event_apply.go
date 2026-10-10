package view

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
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

type periodCompletionInput struct {
	datasetID          string
	frequency          string
	periodTime         int64
	status             string
	universeSubjectIDs []string
	failedSubjects     []string
	expectedScopeRef   string
	committedPositions []*storageeventpb.CommittedPosition
	completionKind     string
}

func (s *Service) HandleCollectorPeriodCompleted(ctx context.Context, message *eventpb.EventMessage, payload *storageeventpb.CollectorPeriodCompleted) error {
	if message == nil || payload == nil {
		return eventconsumer.Permanent(errors.New("Collector 周期完成事件为空"))
	}
	return s.applyPeriodCompletion(ctx, message, periodCompletionInput{
		datasetID: payload.GetDatasetId(), frequency: payload.GetFrequency(), periodTime: payload.GetPeriodTime(),
		status: payload.GetStatus(), universeSubjectIDs: payload.GetUniverseSubjectIds(), failedSubjects: payload.GetFailedSubjects(),
		expectedScopeRef: payload.GetExpectedScopeRef(), committedPositions: payload.GetCommittedPositions(),
		completionKind: events.CollectorPeriodCompleted.Name(),
	})
}

func (s *Service) applyPeriodCompletion(ctx context.Context, message *eventpb.EventMessage, completion periodCompletionInput) error {
	metadata, publisher := s.periodDependencies()
	if metadata == nil || publisher == nil {
		return errors.New("View 处理周期事件所需的依赖尚未就绪")
	}
	views := s.activeViewsForDataset(message.GetSpaceId(), completion.datasetID)
	if len(views) == 0 {
		managed, err := s.datasetHasActiveView(ctx, message.GetSpaceId(), completion.datasetID)
		if err != nil {
			return err
		}
		if managed {
			return errors.New("View 索引映射尚未就绪")
		}
		return nil
	}
	for _, view := range views {
		state := &pb.ViewPeriodDatasetState{
			SpaceId: message.GetSpaceId(), ViewId: view.GetViewId(), DatasetId: completion.datasetID, Frequency: completion.frequency,
			PeriodTime: completion.periodTime, EventId: message.GetEventId(), Status: completion.status,
			SubjectIds: append([]string(nil), completion.universeSubjectIDs...), FailedSubjects: append([]string(nil), completion.failedSubjects...),
			OccurredAt: cloneTimestamp(message.GetOccurredAt()),
		}
		rsp, err := metadata.UpsertViewPeriodDatasetState(ctx, &pb.UpsertViewPeriodDatasetStateReq{AuthInfo: s.internalAuth(), State: state})
		if err != nil {
			return err
		}
		if err := requireStorageSuccess("写入 View 周期的数据集状态", rsp.GetRetInfo()); err != nil {
			return err
		}
		if recorded := rsp.GetState(); recorded != nil && recorded.GetEventId() != message.GetEventId() {
			// 该周期已由更早的上报决定并发布了就绪，这次重复上报不改变任何东西。
			log.Printf("storage view 忽略重复的周期上报 space=%s view=%s dataset=%s freq=%s period=%d event=%s recorded_event=%s",
				message.GetSpaceId(), view.GetViewId(), completion.datasetID, completion.frequency, completion.periodTime, message.GetEventId(), recorded.GetEventId())
			continue
		}
		statesRsp, err := metadata.ListViewPeriodDatasetStates(ctx, &pb.ListViewPeriodDatasetStatesReq{
			AuthInfo: s.internalAuth(), SpaceId: message.GetSpaceId(), ViewId: view.GetViewId(), Frequency: completion.frequency, PeriodTime: completion.periodTime,
		})
		if err != nil {
			return err
		}
		if err := requireStorageSuccess("读取 View 周期的数据集状态", statesRsp.GetRetInfo()); err != nil {
			return err
		}
		seenDatasets := make(map[string]struct{}, len(statesRsp.GetStates()))
		for _, state := range statesRsp.GetStates() {
			if state != nil {
				seenDatasets[state.GetDatasetId()] = struct{}{}
			}
		}
		waiting := 0
		for _, datasetID := range viewDatasetIDs(view) {
			if _, exists := seenDatasets[datasetID]; !exists {
				waiting++
			}
		}
		s.metrics.ObservePeriodWaiting(view.GetViewId(), completion.frequency, waiting)
		ready, ok := viewDataReadyPayload(view, completion, statesRsp.GetStates(), message)
		if !ok {
			continue
		}
		eventID := stableViewEventID("data-ready", message.GetSpaceId(), view.GetViewId(), completion.frequency, strconv.FormatInt(completion.periodTime, 10))
		if err := s.enqueueViewDataReady(view, completion.committedPositions, ready, message, eventID); err != nil {
			return err
		}
		if err := s.FlushViewDataReady(ctx, message.GetSpaceId(), view.GetViewId()); err != nil {
			return err
		}
		if s.pendingReadyContains(eventID) {
			return eventconsumer.Deferred(ErrViewDataReadyPending)
		}
	}
	return nil
}

func (s *Service) HandleFactorPeriodComputed(ctx context.Context, message *eventpb.EventMessage, payload *storageeventpb.FactorPeriodComputed) error {
	if message == nil || payload == nil {
		return eventconsumer.Permanent(errors.New("Factor 周期计算事件为空"))
	}
	_, publisher := s.periodDependencies()
	if publisher == nil {
		return errors.New("View 就绪事件发布器尚未就绪")
	}
	views := s.activeViewsForDataset(message.GetSpaceId(), payload.GetDatasetId())
	if len(views) == 0 {
		managed, err := s.datasetHasActiveView(ctx, message.GetSpaceId(), payload.GetDatasetId())
		if err != nil {
			return err
		}
		if managed {
			return errors.New("View 索引映射尚未就绪")
		}
		return nil
	}
	failed := append([]string(nil), payload.GetFailedSubjects()...)
	for _, state := range payload.GetFactors() {
		if state != nil {
			failed = append(failed, state.GetFailedSubjects()...)
		}
	}
	for _, view := range views {
		ready := &storageeventpb.ViewDataReady{
			ViewId: view.GetViewId(), ViewConfigId: viewConfigID(view), CompletionEventId: message.GetEventId(),
			CompletionKind: events.FactorPeriodComputed.Name(),
			DatasetId:      payload.GetDatasetId(), Status: payload.GetStatus(), VisibleScope: viewVisibleScope(view, "view:"+view.GetViewId()),
			Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(), FailedScopeRef: degradedScopeRef(payload.GetStatus(), failed),
			ReadyAt: cloneTimestamp(message.GetOccurredAt()),
			// 因子状态保留它们冻结的全集，即使 View 过滤了行。
			UniverseSubjectIds: append([]string(nil), payload.GetUniverseSubjectIds()...),
			Factors:            cloneFactorStates(payload.GetFactors()),
		}
		eventID := stableViewEventID("data-ready", message.GetEventId(), view.GetViewId())
		if err := s.enqueueViewDataReady(view, nil, ready, message, eventID); err != nil {
			return err
		}
		if err := s.FlushViewDataReady(ctx, message.GetSpaceId(), view.GetViewId()); err != nil {
			return err
		}
		if s.pendingReadyContains(eventID) {
			return eventconsumer.Deferred(ErrViewDataReadyPending)
		}
	}
	return nil
}

func (s *Service) HandleDatasetSyncPoint(ctx context.Context, message *eventpb.EventMessage, payload *storageeventpb.DatasetSyncPoint) error {
	if message == nil || payload == nil {
		return eventconsumer.Permanent(errors.New("数据集同步点事件为空"))
	}
	metadata, _ := s.periodDependencies()
	if metadata == nil {
		return errors.New("View 处理周期事件所需的元数据客户端尚未就绪")
	}
	views := s.syncPointViewsForDataset(message.GetSpaceId(), payload.GetDatasetId())
	if len(views) == 0 {
		managed, err := s.datasetHasActiveView(ctx, message.GetSpaceId(), payload.GetDatasetId())
		if err != nil {
			return err
		}
		if managed {
			return errors.New("View 索引映射尚未就绪")
		}
		return nil
	}
	for _, view := range views {
		ready, readyErr := s.hasReadyViewIndex(ctx, view)
		if readyErr != nil {
			return readyErr
		}
		if !ready {
			return fmt.Errorf("View %s/%s 没有可用的活动索引或正在构建的索引", view.GetSpaceId(), view.GetViewId())
		}
		rsp, err := metadata.RecordViewSyncPoint(ctx, &pb.RecordViewSyncPointReq{AuthInfo: s.internalAuth(), SyncPoint: &pb.ViewSyncPoint{
			SpaceId: message.GetSpaceId(), ViewId: view.GetViewId(), DatasetId: payload.GetDatasetId(), RequestId: payload.GetRequestId(),
			SyncPointId: payload.GetSyncPointId(), AppliedAt: cloneTimestamp(message.GetOccurredAt()),
		}})
		if err != nil {
			return err
		}
		if err := requireStorageSuccess("记录 View 同步点", rsp.GetRetInfo()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) hasReadyViewIndex(ctx context.Context, view *pb.View) (bool, error) {
	if view == nil {
		return false, nil
	}
	// 单元测试与轻量的嵌入方可以不打开物理索引引擎就演练事件协议。真实的 Service 里，New 总是至少装上 Bleve 引擎，
	// 所以这不会削弱运行时的就绪检查。
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
		// 只有构建中索引的 View，必须先把索引持久化地标记为就绪，才能确认 SyncPoint；否则源围栏已经记录，
		// 一次崩溃就会丢掉这个索引。
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

// syncPointViewsForDataset 包含只有构建中索引的 View。行已经写入那个索引，导入或追平围栏不能为了第一个活动
// 版本永远等下去。
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
		activeID := runtime.active
		active := runtime.active != "" || (includeBuilding && runtime.next != "")
		datasetIDs := append([]string(nil), runtime.activeDatasetIDs...)
		primaryDatasetID := runtime.activePrimaryDatasetID
		if runtime.active == "" && runtime.next != "" {
			datasetIDs = append([]string(nil), runtime.nextDatasetIDs...)
			primaryDatasetID = runtime.nextPrimaryDatasetID
			if len(datasetIDs) == 0 {
				datasetIDs = viewDatasetIDs(candidate.view)
			}
		}
		if !runtime.activeDatasetSet && runtime.active != "" {
			datasetIDs = viewDatasetIDs(candidate.view)
			primaryDatasetID = candidate.view.GetDatasetId()
		}
		if primaryDatasetID == "" {
			primaryDatasetID = candidate.view.GetDatasetId()
		}
		runtime.mu.Unlock()
		contractID := strings.TrimSpace(primaryDatasetID)
		if contractID == "" && len(datasetIDs) > 0 {
			contractID = strings.TrimSpace(datasetIDs[0])
		}
		if active && contractID == datasetID {
			view := candidate.view
			// 携带将接收这个事件的索引的契约。A/B 重建期间目录里的副本可能描述更新的目标版本，不能提前改变源就绪的聚合。
			view.DatasetId = contractID
			if activeID != "" {
				view.ActiveIndexId = activeID
			}
			result = append(result, view)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GetViewId() < result[j].GetViewId() })
	return result
}

func viewDataReadyPayload(view *pb.View, completion periodCompletionInput, states []*pb.ViewPeriodDatasetState, message *eventpb.EventMessage) (*storageeventpb.ViewDataReady, bool) {
	byDataset := make(map[string]*pb.ViewPeriodDatasetState, len(states))
	for _, state := range states {
		if state != nil {
			byDataset[state.GetDatasetId()] = state
		}
	}
	status := "complete"
	var failed []string
	readyAt := cloneTimestamp(message.GetOccurredAt())
	for _, datasetID := range viewDatasetIDs(view) {
		state := byDataset[datasetID]
		if state == nil {
			return nil, false
		}
		if state.GetStatus() == "degraded" {
			status = "degraded"
			failed = append(failed, state.GetFailedSubjects()...)
		}
		if state.GetOccurredAt() != nil && (readyAt == nil || state.GetOccurredAt().AsTime().After(readyAt.AsTime())) {
			readyAt = cloneTimestamp(state.GetOccurredAt())
		}
	}
	return &storageeventpb.ViewDataReady{
		ViewId: view.GetViewId(), ViewConfigId: viewConfigID(view), CompletionEventId: message.GetEventId(),
		CompletionKind: firstNonEmpty(completion.completionKind, message.GetEventName()),
		DatasetId:      completion.datasetID, Status: status, VisibleScope: viewVisibleScope(view, firstNonEmpty(completion.expectedScopeRef, "view:"+view.GetViewId())),
		Frequency: completion.frequency, PeriodTime: completion.periodTime, FailedScopeRef: degradedScopeRef(status, failed),
		CommittedPositions: cloneCommittedPositions(completion.committedPositions), ReadyAt: readyAt,
		UniverseSubjectIds: uniqueSortedStrings(completion.universeSubjectIDs),
	}, true
}

func viewConfigID(view *pb.View) string {
	if view == nil {
		return ""
	}
	if hash := strings.TrimSpace(view.GetActiveViewSchemaHash()); hash != "" {
		return hash
	}
	if view.GetActiveViewRevision() > 0 {
		return fmt.Sprintf("%s@%d", view.GetViewId(), view.GetActiveViewRevision())
	}
	return view.GetViewId()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func degradedScopeRef(status string, failed []string) string {
	if status != "degraded" {
		return ""
	}
	if refs := uniqueSortedStrings(failed); len(refs) > 0 {
		return strings.Join(refs, ",")
	}
	return "degraded"
}

func cloneCommittedPositions(values []*storageeventpb.CommittedPosition) []*storageeventpb.CommittedPosition {
	out := make([]*storageeventpb.CommittedPosition, 0, len(values))
	for _, value := range values {
		if value != nil {
			out = append(out, proto.Clone(value).(*storageeventpb.CommittedPosition))
		}
	}
	return out
}

func cloneFactorStates(values []*storageeventpb.FactorPeriodState) []*storageeventpb.FactorPeriodState {
	out := make([]*storageeventpb.FactorPeriodState, len(values))
	for i, value := range values {
		if value != nil {
			out[i] = proto.Clone(value).(*storageeventpb.FactorPeriodState)
		}
	}
	return out
}

func requireStorageSuccess(operation string, info *pb.RetInfo) error {
	if info == nil {
		return fmt.Errorf("%s 返回的 ret_info 为空", operation)
	}
	if info.GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s 失败：%s", operation, info.GetMsg())
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
