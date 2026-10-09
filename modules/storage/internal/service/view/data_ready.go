package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
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
	if err := s.persistAppliedFence(); err != nil {
		s.readyIssue("View 写入围栏落盘失败：%v", err)
	}
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

// readyPublishTimeout 限制单条就绪事件的发布等待。不设截止时间的 JetStream 发布在确认丢失（EventBus 重启、
// 断网）时会一直挂起；刷新持有串行锁，挂起会让所有 View 的投递排在后面。
var readyPublishTimeout = 10 * time.Second

// readyRetryBackoff 是发布因连接问题失败后的退避：期间刷新直接跳过，不再每次都付出一次超时；重连在后台进行。
var readyRetryBackoff = 5 * time.Second

// orphanReadyAge 是就绪事件所属 View 已不在目录中（被删除或停用）时的保留时长：超过后隔离，不永久占着队列。
const orphanReadyAge = 24 * time.Hour

// permanentReadyPublishError 报告发布错误是否是确定性的：消息无法通过发布前校验或超过 EventBus 的最大消息长度，
// 原样重试不会成功，只能隔离。
func permanentReadyPublishError(err error) bool {
	return errors.Is(err, jetstream.ErrInvalidMessage) || errors.Is(err, nats.ErrMaxPayload)
}

// FlushViewDataReady 按入队顺序发布已满足写入围栏的就绪事件。刷新串行执行：并发刷新会互相越过，
// 打乱同一 View 的周期顺序（下游会把较早的周期记为乱序）。事件只在发布成功（或因无法通过契约校验而隔离）
// 后才移出内存队列，每次刷新结束时落盘一次：进程在发布途中退出不会丢事件，重启后重复发布由 Msg-Id 去重。
// 发布失败时失败项与其后的项按原顺序留在队首；连接问题导致的失败进入退避并在后台重连。
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
	if s.inReadyBackoff() {
		return nil
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	s.pendingReadyMu.Lock()
	pending := append([]pendingViewReady(nil), s.pendingReady...)
	s.pendingReadyMu.Unlock()
	removed := false
	defer func() {
		if removed {
			if err := s.persistPendingReady(); err != nil {
				log.Printf("View 就绪队列落盘失败：%v", err)
			}
		}
	}()
	for _, item := range pending {
		if spaceID != "" && (item.spaceID != spaceID || (viewID != "" && item.viewID != viewID)) {
			continue
		}
		view := s.viewSnapshot(item.spaceID, item.viewID)
		if view == nil {
			if !item.opts.OccurredAt.IsZero() && time.Since(item.opts.OccurredAt) > orphanReadyAge {
				log.Printf("View 就绪队列隔离所属 View 已不存在的事件 %s（%s/%s）", item.opts.EventID, item.spaceID, item.viewID)
				s.removePending(item.opts.EventID)
				removed = true
			}
			continue
		}
		if !s.positionsApplied(view, item.required) {
			continue
		}
		if _, err := registry.Encode(events.ViewDataReady, item.payload, item.opts); err != nil {
			log.Printf("View 就绪队列隔离无法通过契约校验的事件 %s（%s/%s）：%v", item.opts.EventID, item.spaceID, item.viewID, err)
			s.removePending(item.opts.EventID)
			removed = true
			continue
		}
		publishCtx, cancel := context.WithTimeout(ctx, readyPublishTimeout)
		_, err := publisher.Publish(publishCtx, events.ViewDataReady, item.payload, item.opts)
		cancel()
		if err != nil {
			// 调用方已结束（进程关闭、消费者重启）不是连接故障：不退避，也不重连一条健康的连接。
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if s.metrics != nil {
				s.metrics.ObserveReadyPublishRetry(item.viewID, "view_data_ready")
			}
			if permanentReadyPublishError(err) {
				log.Printf("View 就绪队列隔离无法发布的事件 %s（%s/%s）：%v", item.opts.EventID, item.spaceID, item.viewID, err)
				s.removePending(item.opts.EventID)
				removed = true
				continue
			}
			if errors.Is(err, jetstream.ErrPublishTimeout) || errors.Is(err, jetstream.ErrConnection) || errors.Is(err, jetstream.ErrClosed) {
				s.startReadyBackoff(err)
			}
			return err
		}
		s.removePending(item.opts.EventID)
		removed = true
	}
	s.readyRecovered()
	return nil
}

// readyIssue 记录就绪队列的一条异常；与上一条相同则不重复记录，避免持续故障时每 5 秒一行。
func (s *Service) readyIssue(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	s.readyLogMu.Lock()
	repeated := message == s.readyLastIssue
	s.readyLastIssue = message
	s.readyLogMu.Unlock()
	if !repeated {
		log.Print(message)
	}
}

// readyRecovered 在一次刷新正常完成后记录恢复。
func (s *Service) readyRecovered() {
	s.readyLogMu.Lock()
	had := s.readyLastIssue != ""
	s.readyLastIssue = ""
	s.readyLogMu.Unlock()
	if had {
		log.Print("View 就绪事件发布已恢复")
	}
}

func (s *Service) inReadyBackoff() bool {
	s.mu.RLock()
	until := s.readyBackoffUntil
	s.mu.RUnlock()
	backoff := time.Now().Before(until)
	if !backoff && s.metrics != nil {
		s.metrics.SetReadyPublishBackoff(false)
	}
	return backoff
}

// startReadyBackoff 进入退避，并在刷新锁之外重连就绪发布器的专用连接：连接可能已静默断开或已被关闭，
// 等心跳发现要几分钟，被关闭的连接则永远不会自行恢复。
func (s *Service) startReadyBackoff(cause error) {
	s.mu.Lock()
	s.readyBackoffUntil = time.Now().Add(readyRetryBackoff)
	reconnect := s.readyReconnect
	s.mu.Unlock()
	if s.metrics != nil {
		s.metrics.SetReadyPublishBackoff(true)
	}
	s.readyIssue("View 就绪事件发布失败，进入 %s 退避：%v", readyRetryBackoff, cause)
	if reconnect == nil || !s.readyReconnecting.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.readyReconnecting.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), readyPublishTimeout)
		defer cancel()
		if err := reconnect(ctx); err != nil {
			s.readyIssue("View 就绪事件发布器重连 EventBus 失败：%v", err)
		}
	}()
}

// removePending 把已发布或已隔离的事件移出内存队列；落盘由刷新结束时统一完成。
func (s *Service) removePending(eventID string) {
	s.pendingReadyMu.Lock()
	defer s.pendingReadyMu.Unlock()
	for i, item := range s.pendingReady {
		if item.opts.EventID == eventID {
			s.pendingReady = append(s.pendingReady[:i], s.pendingReady[i+1:]...)
			s.observeReadyQueueLocked()
			return
		}
	}
}

// observeReadyQueueLocked 上报就绪队列的深度与最老事件的时间；调用方持有 pendingReadyMu。
func (s *Service) observeReadyQueueLocked() {
	if s.metrics == nil {
		return
	}
	var oldest time.Time
	for _, item := range s.pendingReady {
		if at := item.opts.OccurredAt; !at.IsZero() && (oldest.IsZero() || at.Before(oldest)) {
			oldest = at
		}
	}
	s.metrics.SetReadyQueue(len(s.pendingReady), oldest)
}

// hasPendingReady 报告就绪队列里是否还有待发布的事件。
func (s *Service) hasPendingReady() bool {
	s.pendingReadyMu.Lock()
	defer s.pendingReadyMu.Unlock()
	return len(s.pendingReady) > 0
}

// SetSeriesBars 设置时序 View 每个序列保留的根数（0 表示默认值），随查询响应告知调用方；关闭维护时也要设置。
func (s *Service) SetSeriesBars(bars uint64) {
	if bars == 0 {
		bars = defaultViewBars
	}
	s.mu.Lock()
	s.seriesBars = bars
	s.mu.Unlock()
}

// enqueueViewDataReady 把就绪事件放入队列并落盘；落盘失败时返回错误，调用方不 ACK 周期事件，由重投再次入队。
func (s *Service) enqueueViewDataReady(view *pb.View, required []*storagepb.CommittedPosition, payload *storagepb.ViewDataReady, message *eventpb.EventMessage, eventID string) error {
	if view == nil || payload == nil || message == nil || strings.TrimSpace(eventID) == "" {
		return nil
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
			return nil
		}
	}
	s.pendingReady = append(s.pendingReady, item)
	s.observeReadyQueueLocked()
	if err := s.persistPendingReadyLocked(); err != nil {
		return fmt.Errorf("View 就绪队列落盘失败：%w", err)
	}
	return nil
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
