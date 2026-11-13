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
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

// NoticePublisher 把补写通知合并成批写入本地发件箱；发件箱里的事件由 Collector 级的中继
// （eventbus.Relay）发布 event.collector.kline.backfilled。
//
// 一批通知的 ID 由通知 ID 确定性算出：崩溃后重放得到同一个事件 ID，JetStream 用 Msg-Id 去重。
type NoticePublisher struct {
	State    *store.ResampleStateRepository
	Registry *events.Registry
	// Enabled 为假表示没有配置事件总线：通知只保留到保留期，不成批。
	Enabled bool
	// Wake 在封存出新事件后调用，让中继不等下一个轮询周期就发布。
	Wake func()
	Now  func() time.Time
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
	seriesTags                                   map[string]struct{}
	start, end                                   time.Time
}

// Flush 合并并封存全部还没成批的通知，写入发件箱。
func (p *NoticePublisher) Flush(ctx context.Context) error {
	if p == nil || p.State == nil {
		return errors.New("notice publisher state is required")
	}
	if !p.Enabled || p.Registry == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.seal(ctx)
	if p.Wake != nil {
		p.Wake()
	}
	return err
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
				subjects: map[string]struct{}{}, seriesTags: map[string]struct{}{}, start: notice.Start, end: notice.End}
			groups[key] = group
			order = append(order, key)
		}
		group.ids = append(group.ids, notice.ID)
		group.subjects[notice.SubjectID] = struct{}{}
		if notice.SeriesTag != "" {
			group.seriesTags[notice.SeriesTag] = struct{}{}
		}
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
	subjects := make([]string, 0, len(group.subjects))
	for subject := range group.subjects {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	// 只有统一数据集的通知带序列，单序列数据集的事件不带 series_tags。
	var seriesTags []string
	for tag := range group.seriesTags {
		seriesTags = append(seriesTags, tag)
	}
	sort.Strings(seriesTags)
	now := p.now()
	payload := &storagepb.CollectorKlineBackfilled{
		DatasetId: group.datasetID, Frequency: group.frequency, SubjectIds: subjects,
		Start: timestamppb.New(group.start), End: timestamppb.New(group.end), Origin: group.origin, JobId: group.jobID,
		ConfirmedAt: timestamppb.New(now), SeriesTags: seriesTags,
	}
	raw, err := p.Registry.MarshalMessage(events.CollectorKlineBackfilled, payload, events.PublishOptions{
		EventID: id, OccurredAt: now, SpaceID: group.spaceID, SubjectID: group.datasetID,
	})
	if err != nil {
		return fmt.Errorf("encode backfilled event: %w", err)
	}
	return p.State.SealBatch(ctx, group.ids, store.OutboxEvent{EventID: id, SpaceID: group.spaceID, EventName: events.CollectorKlineBackfilled.Name(), SubjectToken: group.datasetID, Payload: raw})
}
