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

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	"trpc.group/trpc-go/trpc-go/log"
)

// publishFollowMarker 为一条完成标记发布 ViewDataReady。轮到这条标记时，数据集在它之前的行都已写入索引。
// 标记解不开或事件无法通过契约校验时记日志跳过：原样重试不会成功，不能让这个 View 永远卡住。
func (s *Service) publishFollowMarker(ctx context.Context, ref datasetRef, change *pb.DatasetChange) error {
	publisher := s.readyPublisherLocked()
	if publisher == nil {
		return errors.New("View 就绪事件发布器尚未就绪")
	}
	message := &eventpb.EventMessage{}
	payload := &storageeventpb.DatasetPeriodCompleted{}
	if err := proto.Unmarshal(change.GetMarker(), message); err != nil {
		log.Errorf("storage view 跳过无法解析的完成标记 view=%s/%s 序号=%d：%v", ref.spaceID, ref.datasetID, change.GetSequence(), err)
		return nil
	}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		log.Errorf("storage view 跳过无法解析的完成标记 view=%s/%s 序号=%d 事件=%s：%v", ref.spaceID, ref.datasetID, change.GetSequence(), message.GetEventId(), err)
		return nil
	}
	runtime := s.runtimeOf(ref)
	if runtime == nil {
		return fmt.Errorf("View %s/%s 不在目录中", ref.spaceID, ref.datasetID)
	}
	ready, eventID, ok := followReadyEvent(ref, message, payload)
	if !ok {
		log.Errorf("storage view 跳过来源不认识的完成标记 view=%s/%s 序号=%d 来源=%q", ref.spaceID, ref.datasetID, change.GetSequence(), payload.GetSource())
		return nil
	}
	opts := events.PublishOptions{EventID: eventID, OccurredAt: time.UnixMilli(ready.GetReadyAt()).UTC(), SpaceID: ref.spaceID, SubjectID: ref.datasetID}
	// 先按契约编码一遍：过不了校验的事件原样重试也不会成功（发布器返回的校验错误不能可靠地区分），记日志跳过。
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	if _, err := registry.Encode(events.ViewDataReady, ready, opts); err != nil {
		log.Errorf("storage view 跳过无法通过契约校验的就绪事件 %s（%s/%s 第 %d 期）：%v", eventID, ref.spaceID, ref.datasetID, payload.GetPeriodTime(), err)
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
		s.metrics.ObserveReadyPublishRetry(ref.spaceID, ref.datasetID, "view_data_ready")
		if permanentReadyPublishError(err) {
			log.Errorf("storage view 跳过无法发布的就绪事件 %s（%s/%s 第 %d 期）：%v", eventID, ref.spaceID, ref.datasetID, payload.GetPeriodTime(), err)
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
		ref.spaceID, ref.datasetID, payload.GetSource(), payload.GetPeriodTime(), sinceCommit.Round(time.Millisecond), time.Since(publishStart).Round(time.Millisecond))
	return nil
}

// followReadyEvent 由完成标记组装 ViewDataReady 与它的事件 ID（下游按事件 ID 去重）。
// View 只读一个数据集，账本判定的完成直接就是 View 的就绪；事件的 subject_id 是数据集 ID。
func followReadyEvent(ref datasetRef, message *eventpb.EventMessage, payload *storageeventpb.DatasetPeriodCompleted) (*storageeventpb.ViewDataReady, string, bool) {
	ready := &storageeventpb.ViewDataReady{
		CompletionEventId: message.GetEventId(), DatasetId: ref.datasetID, Status: payload.GetStatus(),
		Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(),
		FailedSubjects: uniqueSortedStrings(payload.GetFailedSubjects()),
		ReadyAt:        message.GetOccurredAt(),
	}
	switch payload.GetSource() {
	case "ledger":
		ready.UniverseSubjectIds = uniqueSortedStrings(payload.GetUniverseSubjectIds())
		return ready, stableViewEventID("data-ready", message.GetSpaceId(), ref.datasetID, payload.GetFrequency(), strconv.FormatInt(payload.GetPeriodTime(), 10)), true
	case "producer":
		// 写入方冻结的标的全集，即使 View 过滤了行；明细原样透传。
		ready.UniverseSubjectIds = append([]string(nil), payload.GetUniverseSubjectIds()...)
		ready.DetailType, ready.Detail = payload.GetDetailType(), append([]byte(nil), payload.GetDetail()...)
		return ready, stableViewEventID("data-ready", message.GetEventId(), ref.datasetID), true
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

func (s *Service) readyPublisherLocked() ReadyEventPublisher {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readyPublisher
}

func stableViewEventID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "storage-view-" + hex.EncodeToString(hash[:16])
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
