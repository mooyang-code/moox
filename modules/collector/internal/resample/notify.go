package resample

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

// DatasetSyncPoints 在数据集的有序 outbox 里追加同步点：View 处理到它，之前的行都已写入索引。
type DatasetSyncPoints interface {
	AppendDatasetSyncPoint(ctx context.Context, spaceID, datasetID, requestID, source string) error
}

// EventRelay 把已编码的事件发布到 JetStream；events.Publisher 实现它。
type EventRelay interface {
	PublishMessage(context.Context, *eventpb.EventMessage) (*jetstream.PublishAck, error)
}

// NoticePublisher 把补写通知合并成批，对源数据集追加同步点，再经本地发件箱发布 event.collector.kline.backfilled。
//
// 一批通知的 ID 由通知 ID 确定性算出：崩溃后重放得到同一个同步点与事件 ID，JetStream 用 Msg-Id 去重。
type NoticePublisher struct {
	State    *store.ResampleStateRepository
	Sync     DatasetSyncPoints
	Registry *events.Registry
	// Relay 为空表示没有事件总线：通知只保留到保留期，不发布。
	Relay EventRelay
	Now   func() time.Time
	// MaxNotices 是一次合并的通知数上限。
	MaxNotices int

	mu sync.Mutex
}

func (p *NoticePublisher) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

type noticeGroup struct {
	spaceID, datasetID, frequency, origin, jobID string
	ids                                          []int64
	subjects                                     map[string]struct{}
	start, end                                   time.Time
}

// Flush 合并并封存全部还没成批的通知，然后发布发件箱里的事件。
func (p *NoticePublisher) Flush(ctx context.Context) error {
	if p == nil || p.State == nil {
		return errors.New("notice publisher state is required")
	}
	if p.Relay == nil || p.Registry == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []error
	if err := p.seal(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := p.relay(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (p *NoticePublisher) seal(ctx context.Context) error {
	limit := p.MaxNotices
	if limit <= 0 {
		limit = 5000
	}
	notices, err := p.State.UnsealedNotices(ctx, limit)
	if err != nil || len(notices) == 0 {
		return err
	}
	groups := map[string]*noticeGroup{}
	var order []string
	for _, notice := range notices {
		key := strings.Join([]string{notice.SpaceID, notice.DatasetID, notice.Frequency, notice.Origin, notice.JobID}, "\x00")
		group, ok := groups[key]
		if !ok {
			group = &noticeGroup{spaceID: notice.SpaceID, datasetID: notice.DatasetID, frequency: notice.Frequency, origin: notice.Origin, jobID: notice.JobID,
				subjects: map[string]struct{}{}, start: notice.Start, end: notice.End}
			groups[key] = group
			order = append(order, key)
		}
		group.ids = append(group.ids, notice.ID)
		group.subjects[notice.SubjectID] = struct{}{}
		if notice.Start.Before(group.start) {
			group.start = notice.Start
		}
		if notice.End.After(group.end) {
			group.end = notice.End
		}
	}
	var errs []error
	for _, key := range order {
		if err := p.sealGroup(ctx, groups[key]); err != nil {
			log.WarnContextf(ctx, "[Collector] seal backfill notices failed dataset=%s: %v", groups[key].datasetID, err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func batchID(ids []int64) string {
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	digest := sha256.New()
	for _, id := range sorted {
		_, _ = digest.Write([]byte(strconv.FormatInt(id, 10)))
		_, _ = digest.Write([]byte{','})
	}
	return "kbf-" + hex.EncodeToString(digest.Sum(nil))[:32]
}

func (p *NoticePublisher) sealGroup(ctx context.Context, group *noticeGroup) error {
	id := batchID(group.ids)
	// 先追加同步点：事件发出时，读 View 的消费者可以等 View 追上它。同一批重放得到同一个 request_id。
	if p.Sync != nil {
		if err := p.Sync.AppendDatasetSyncPoint(ctx, group.spaceID, group.datasetID, id, "catchup"); err != nil {
			return fmt.Errorf("append sync point: %w", err)
		}
	}
	subjects := make([]string, 0, len(group.subjects))
	for subject := range group.subjects {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	now := p.now()
	payload := &storagepb.CollectorKlineBackfilled{
		DatasetId: group.datasetID, Frequency: group.frequency, SubjectIds: subjects,
		Start: timestamppb.New(group.start), End: timestamppb.New(group.end), Origin: group.origin, JobId: group.jobID,
		SyncRequestId: id, ConfirmedAt: timestamppb.New(now),
	}
	raw, err := p.Registry.MarshalMessage(events.CollectorKlineBackfilled, payload, events.PublishOptions{
		EventID: id, OccurredAt: now, SpaceID: group.spaceID, SubjectID: group.datasetID,
	})
	if err != nil {
		return fmt.Errorf("encode backfilled event: %w", err)
	}
	return p.State.SealBatch(ctx, group.ids, store.OutboxEvent{EventID: id, SpaceID: group.spaceID, EventName: events.CollectorKlineBackfilled.Name(), SubjectToken: group.datasetID, Payload: raw})
}

func (p *NoticePublisher) relay(ctx context.Context) error {
	pending, err := p.State.PendingEvents(ctx, 100)
	if err != nil {
		return err
	}
	for _, event := range pending {
		message, err := p.Registry.UnmarshalMessage(event.Payload)
		if err != nil {
			return fmt.Errorf("outbox event %s is invalid: %w", event.EventID, err)
		}
		if _, err := p.Relay.PublishMessage(ctx, message); err != nil {
			return fmt.Errorf("publish backfilled event %s: %w", event.EventID, err)
		}
		if err := p.State.MarkPublished(ctx, event.ID); err != nil {
			return err
		}
	}
	return nil
}
