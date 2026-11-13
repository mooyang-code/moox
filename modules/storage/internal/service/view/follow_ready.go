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
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"trpc.group/trpc-go/trpc-go/log"
)

// publishFollowMarker 为一条完成标记发布 ViewDataReady。轮到这条标记时，数据集在它之前的行都已写入索引。
// 标记解不开或事件无法通过契约校验时记日志跳过：原样重试不会成功，不能让这个 View 永远卡住。
func (s *Service) publishFollowMarker(ctx context.Context, ref viewRef, change *pb.DatasetChange) error {
	publisher := s.readyPublisherLocked()
	if publisher == nil {
		return errors.New("View 就绪事件发布器尚未就绪")
	}
	message := &eventpb.EventMessage{}
	payload := &storageeventpb.DatasetPeriodCompleted{}
	if err := proto.Unmarshal(change.GetMarker(), message); err != nil {
		log.Errorf("storage view 跳过无法解析的完成标记 view=%s/%s 序号=%d：%v", ref.spaceID, ref.viewID, change.GetSequence(), err)
		return nil
	}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		log.Errorf("storage view 跳过无法解析的完成标记 view=%s/%s 序号=%d 事件=%s：%v", ref.spaceID, ref.viewID, change.GetSequence(), message.GetEventId(), err)
		return nil
	}
	view, schema := s.viewSnapshot(ref.spaceID, ref.viewID)
	if view == nil {
		return fmt.Errorf("View %s/%s 不在目录中", ref.spaceID, ref.viewID)
	}
	ready, eventID, ok := followReadyEvent(view, viewConfigID(view.GetViewId(), schema), message, payload)
	if !ok {
		log.Errorf("storage view 跳过来源不认识的完成标记 view=%s/%s 序号=%d 来源=%q", ref.spaceID, ref.viewID, change.GetSequence(), payload.GetSource())
		return nil
	}
	opts := events.PublishOptions{EventID: eventID, OccurredAt: ready.GetReadyAt().AsTime().UTC(), SpaceID: view.GetSpaceId(), SubjectID: view.GetViewId()}
	// 先按契约编码一遍：过不了校验的事件原样重试也不会成功（发布器返回的校验错误不能可靠地区分），记日志跳过。
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	if _, err := registry.Encode(events.ViewDataReady, ready, opts); err != nil {
		log.Errorf("storage view 跳过无法通过契约校验的就绪事件 %s（%s/%s 第 %d 期）：%v", eventID, view.GetSpaceId(), view.GetViewId(), payload.GetPeriodTime(), err)
		return nil
	}
	publishStart := time.Now()
	publishCtx, cancel := context.WithTimeout(ctx, readyPublishTimeout)
	_, err = publisher.Publish(publishCtx, events.ViewDataReady, ready, opts)
	cancel()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		s.metrics.ObserveReadyPublishRetry(view.GetViewId(), "view_data_ready")
		if permanentReadyPublishError(err) {
			log.Errorf("storage view 跳过无法发布的就绪事件 %s（%s/%s 第 %d 期）：%v", eventID, view.GetSpaceId(), view.GetViewId(), payload.GetPeriodTime(), err)
			return nil
		}
		if errors.Is(err, jetstream.ErrPublishTimeout) || errors.Is(err, jetstream.ErrConnection) || errors.Is(err, jetstream.ErrClosed) {
			s.reconnectReadyPublisher(ctx)
		}
		return err
	}
	sinceCommit := time.Duration(0)
	if committedAt, err := time.Parse(time.RFC3339Nano, change.GetCreatedAt()); err == nil {
		sinceCommit = time.Since(committedAt)
	}
	log.Infof("storage view 周期就绪 view=%s/%s 来源=%s 期=%d 标记提交到发布=%s 发布=%s",
		view.GetSpaceId(), view.GetViewId(), payload.GetSource(), payload.GetPeriodTime(), sinceCommit.Round(time.Millisecond), time.Since(publishStart).Round(time.Millisecond))
	return nil
}

// followReadyEvent 由完成标记组装 ViewDataReady 与它的事件 ID（下游按事件 ID 去重）。
// View 只读一个数据集，账本判定的完成直接就是 View 的就绪，不再查各输入数据集的状态。
func followReadyEvent(view *pb.View, configID string, message *eventpb.EventMessage, payload *storageeventpb.DatasetPeriodCompleted) (*storageeventpb.ViewDataReady, string, bool) {
	ready := &storageeventpb.ViewDataReady{
		ViewId: view.GetViewId(), ViewConfigId: configID, CompletionEventId: message.GetEventId(),
		DatasetId: payload.GetDatasetId(), Status: payload.GetStatus(),
		Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(),
		FailedScopeRef: degradedScopeRef(payload.GetStatus(), payload.GetFailedSubjects()),
		ReadyAt:        cloneTimestamp(message.GetOccurredAt()),
	}
	switch payload.GetSource() {
	case "ledger":
		ready.VisibleScope = viewVisibleScope(view, firstNonEmpty(payload.GetSnapshotRef(), "view:"+view.GetViewId()))
		ready.UniverseSubjectIds = uniqueSortedStrings(payload.GetUniverseSubjectIds())
		return ready, stableViewEventID("data-ready", message.GetSpaceId(), view.GetViewId(), payload.GetFrequency(), strconv.FormatInt(payload.GetPeriodTime(), 10)), true
	case "producer":
		ready.VisibleScope = viewVisibleScope(view, "view:"+view.GetViewId())
		// 写入方冻结的标的全集，即使 View 过滤了行；明细原样透传。
		ready.UniverseSubjectIds = append([]string(nil), payload.GetUniverseSubjectIds()...)
		ready.DetailType, ready.Detail = payload.GetDetailType(), append([]byte(nil), payload.GetDetail()...)
		return ready, stableViewEventID("data-ready", message.GetEventId(), view.GetViewId()), true
	default:
		return nil, "", false
	}
}

// reconnectReadyPublisher 在后台替换就绪事件发布器的连接：连接可能已静默断开或被关闭，等心跳发现要几分钟。同一时刻只有一个重连。
func (s *Service) reconnectReadyPublisher(ctx context.Context) {
	s.mu.RLock()
	reconnect := s.readyReconnect
	s.mu.RUnlock()
	if reconnect == nil || !s.readyReconnecting.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.readyReconnecting.Store(false)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readyPublishTimeout)
		defer cancel()
		if err := reconnect(ctx); err != nil {
			log.Errorf("View 就绪事件发布器重连 EventBus 失败：%v", err)
			return
		}
		log.Info("View 就绪事件发布器已重新连接 EventBus")
	}()
}

// rebuildFollowIndex 在断档（View 停机超过日志保留期，或存储已重建）或索引没有跟随进度时原地重建索引：
// 沿用当前活动索引的列契约（修订号、哈希不变），先取开始建时的头部 H_start、读存量，建成时记下 H_done。
// 重建期间 View 不可读；记录型 View 重建后为空，只跟随之后的写入。
func (s *Service) rebuildFollowIndex(ctx context.Context, ref viewRef) (viewindex.FollowState, error) {
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	manager := s.followManager()
	if manager == nil {
		return viewindex.FollowState{}, errors.New("View 没有在跟随变更日志")
	}
	s.mu.RLock()
	catalogView := s.catalogViews[ref]
	runtime := s.views[ref]
	opts := s.maintenance
	if opts.PrimaryRange == nil {
		opts.PrimaryRange = s.primaryRange
	}
	s.mu.RUnlock()
	if catalogView == nil || runtime == nil {
		return viewindex.FollowState{}, fmt.Errorf("View %s/%s 不在目录中", ref.spaceID, ref.viewID)
	}
	view := proto.Clone(catalogView).(*pb.View)
	start, err := manager.Head(ctx, view.GetSpaceId(), view.GetDatasetId())
	if err != nil {
		return viewindex.FollowState{}, err
	}
	runtime.mu.Lock()
	indexID := runtime.active
	if indexID == "" {
		// 上一次重建没有完成：接着用它的索引标识。
		indexID = runtime.building
	}
	s.mu.RLock()
	schema := s.schemas[indexID]
	engineName := s.indexEngine[indexID]
	s.mu.RUnlock()
	engine := s.engines[engineName]
	if indexID == "" || engine == nil || schema.SchemaHash == "" {
		runtime.mu.Unlock()
		return viewindex.FollowState{}, fmt.Errorf("View %s/%s 没有可重建的索引", ref.spaceID, ref.viewID)
	}
	bars := viewBars(view, opts)
	schema.RowsPerSeries = min(bars, maxLatestPerSeries)
	// 建成之前不可读：查询、加列、删除最旧数据都只看活动索引；维护流程看到 building 时不会把它挂回去。
	runtime.clearActiveLocked()
	runtime.building = indexID
	release, err := s.indexWriteGate(indexID).lock(ctx)
	if err == nil {
		err = engine.Remove(ctx, indexID)
		if err == nil {
			err = engine.Prepare(ctx, indexID, schema)
		}
		release()
	}
	runtime.mu.Unlock()
	if err != nil {
		return viewindex.FollowState{}, fmt.Errorf("重新准备索引 %q：%w", indexID, err)
	}
	s.nextIndexRevision(indexID)
	if err := s.backfillInitialIndex(ctx, view, schema, indexID, opts, bars); err != nil {
		return viewindex.FollowState{}, err
	}
	done, err := manager.Head(ctx, view.GetSpaceId(), view.GetDatasetId())
	if err != nil {
		return viewindex.FollowState{}, err
	}
	state := viewindex.FollowState{Cursor: start, BuiltSequence: done.Sequence}
	if err := s.saveFollowState(ctx, indexID, state); err != nil {
		return viewindex.FollowState{}, err
	}
	runtime.mu.Lock()
	if runtime.building != indexID {
		runtime.mu.Unlock()
		return viewindex.FollowState{}, errViewBuildCancelled
	}
	s.attachActiveViewLocked(view, runtime, indexID, schema, engineName)
	runtime.mu.Unlock()
	s.observeActivatedViewWatermark(view, indexID)
	return state, nil
}

// ReadyEventPublisher 发布 ViewDataReady。
type ReadyEventPublisher interface {
	Publish(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error)
}

// readyPublishTimeout 限制单条就绪事件的发布等待：不设截止时间的 JetStream 发布在确认丢失（EventBus 重启、断网）时会一直挂起。
var readyPublishTimeout = 10 * time.Second

// permanentReadyPublishError 报告发布错误是否是确定性的：消息无法通过发布前校验或超过 EventBus 的最大消息长度，原样重试不会成功。
func permanentReadyPublishError(err error) bool {
	return errors.Is(err, jetstream.ErrInvalidMessage) || errors.Is(err, nats.ErrMaxPayload)
}

// SetSeriesBars 设置时序 View 每个序列保留的根数（0 表示默认值），随查询响应告知调用方；关闭维护时也要设置。
func (s *Service) SetSeriesBars(bars uint64) {
	if bars == 0 {
		bars = storagepolicy.DefaultViewBars
	}
	s.mu.Lock()
	s.seriesBars = bars
	s.mu.Unlock()
}

// viewSnapshot 返回目录里 View 的副本与活动索引的契约；活动索引取查询路径的无锁副本，不等运行时锁。
func (s *Service) viewSnapshot(spaceID, viewID string) (*pb.View, viewindex.ViewIndexSchema) {
	s.mu.RLock()
	view := s.catalogViews[viewRef{spaceID: spaceID, viewID: viewID}]
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if view == nil {
		return nil, viewindex.ViewIndexSchema{}
	}
	var schema viewindex.ViewIndexSchema
	if runtime != nil {
		if active := runtime.queryState().active; active != "" {
			s.mu.RLock()
			schema = s.schemas[active]
			s.mu.RUnlock()
		}
	}
	return proto.Clone(view).(*pb.View), schema
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

// viewConfigID 是就绪事件里的 View 配置标识：活动索引的列契约哈希，没有时退回 View ID。
func viewConfigID(viewID string, schema viewindex.ViewIndexSchema) string {
	if hash := strings.TrimSpace(schema.SchemaHash); hash != "" {
		return hash
	}
	return viewID
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
