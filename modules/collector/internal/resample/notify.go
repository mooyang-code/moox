package resample

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"trpc.group/trpc-go/trpc-go/log"
)

// NoticePublisher 把补写通知（补采分块确认、合成补写）合并成批写入本地发件箱；发件箱里的事件由 Collector 级的中继
// （eventbus.Relay）发布 event.collector.kline.backfilled。合成和因子引擎都只订阅这一个事件。
//
// 一批通知的 ID 由通知 ID 确定性算出：崩溃后重放得到同一个事件 ID，JetStream 用 Msg-Id 去重。
type NoticePublisher struct {
	State    *store.ResampleStateRepository
	Registry *events.Registry
	// Absences 非空时，补采来源的事件带上范围内已确认“行情源没有”的 K 线（因子据此不把它们算进缺失比例）。
	Absences LatestAbsenceSource
	// Enabled 为假表示没有配置事件总线：通知只保留到保留期，不成批。
	Enabled bool
	// Wake 在封存出新事件后调用，让中继不等下一个轮询周期就发布。
	Wake func()
	Now  func() time.Time
	// MaxNotices 是一次合并的通知数上限。
	MaxNotices int
	// MaxAbsences 是一个事件最多带的确认缺失段数，不大于 0 时用 maxEventAbsences。
	MaxAbsences int

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

// Flush 合并并封存全部还没成批的通知，写入发件箱。
func (p *NoticePublisher) Flush(ctx context.Context) error {
	return p.flush(ctx, time.Time{})
}

// FlushOlderThan 只封存创建时间早于 before 的通知（维护轮的兜底用）：进行中的补采作业刚写下的通知留给作业结束时一并封存，
// 不会在长的历史补采期间每轮出一个事件。
func (p *NoticePublisher) FlushOlderThan(ctx context.Context, before time.Time) error {
	return p.flush(ctx, before)
}

func (p *NoticePublisher) flush(ctx context.Context, before time.Time) error {
	if p == nil || p.State == nil {
		return errors.New("notice publisher state is required")
	}
	if !p.Enabled || p.Registry == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	limit := p.MaxNotices
	if limit <= 0 {
		limit = 5000
	}
	// 一次取满上限说明后面可能还有：接着封存，直到取回不足上限。出错就停，留给下一次（否则封存不掉的通知会让循环停不下来）。
	var err error
	for taken := limit; taken == limit && err == nil; {
		taken, err = p.seal(ctx, before, limit)
	}
	if p.Wake != nil {
		p.Wake()
	}
	return err
}

// seal 封存一批通知，返回这次取回的条数；before 非零时只取创建时间早于它的。
func (p *NoticePublisher) seal(ctx context.Context, before time.Time, limit int) (int, error) {
	notices, err := p.State.UnsealedNoticesBefore(ctx, before, limit)
	if err != nil || len(notices) == 0 {
		return 0, err
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
	return len(notices), errors.Join(errs...)
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
	// 数据集里每个标的只有一条序列，事件按标的给出范围。
	now := p.now()
	var err error
	payload := &eventpb.CollectorKlineBackfilled{
		DatasetId: group.datasetID, Frequency: group.frequency, SubjectIds: subjects,
		Start: group.start.Unix(), End: group.end.Unix(), Origin: group.origin, JobId: group.jobID,
		ConfirmedAt: now.UnixMilli(),
	}
	if payload.Absent, err = p.absent(ctx, group); err != nil {
		return err
	}
	raw, err := p.Registry.MarshalMessage(events.CollectorKlineBackfilled, payload, events.PublishOptions{
		EventID: id, OccurredAt: now, SpaceID: group.spaceID, SubjectID: group.datasetID,
	})
	if err != nil {
		return fmt.Errorf("encode backfilled event: %w", err)
	}
	return p.State.SealBatch(ctx, group.ids, store.OutboxEvent{EventID: id, SpaceID: group.spaceID, EventName: events.CollectorKlineBackfilled.Name(), SubjectToken: group.datasetID, Payload: raw})
}

// maxEventAbsences 是一个事件最多带的确认缺失段数（够全部 A 股标的各带一段）：超出时只留最新的并告警。
// 跨度长的作业每一批事件都会带范围内的缺失段，上限定得小，发件箱不会被放大。
// 因子只在推进最近的周期时用它扣缺失比例，更早的段用不上。
const maxEventAbsences = 5000

// LatestAbsenceSource 查询一个数据集在范围内已确认“行情源没有”的 K 线区间里最新的若干段（从新到旧）。
type LatestAbsenceSource interface {
	LatestAbsencesIn(ctx context.Context, spaceID, datasetID, frequency string, from, to time.Time, limit int) ([]store.SourceAbsence, error)
}

// absent 取出这批补采范围内全部标的已确认“行情源没有”的 K 线（不限于这批被补写的标的：因子判断某一期能否放行时
// 要看到这一期所有确认缺失的标的），截到 [start, end) 之内。合成来源的通知没有确认缺失。
func (p *NoticePublisher) absent(ctx context.Context, group *noticeGroup) ([]*eventpb.CollectorKlineAbsent, error) {
	if p.Absences == nil || group.origin == store.NoticeOriginResample {
		return nil, nil
	}
	limit := p.MaxAbsences
	if limit <= 0 {
		limit = maxEventAbsences
	}
	// 多取一条只为判断是否超出上限。
	absences, err := p.Absences.LatestAbsencesIn(ctx, group.spaceID, group.datasetID, group.frequency, group.start, group.end, limit+1)
	if err != nil {
		return nil, fmt.Errorf("load source absences: %w", err)
	}
	if len(absences) > limit {
		log.WarnContextf(ctx, "[Collector] backfilled event carries only the latest %d source absences dataset=%s job=%s", limit, group.datasetID, group.jobID)
		absences = absences[:limit]
	}
	out := make([]*eventpb.CollectorKlineAbsent, 0, len(absences))
	for _, absence := range absences {
		from, to := absence.From, absence.To
		if from.Before(group.start) {
			from = group.start
		}
		if to.After(group.end) {
			to = group.end
		}
		out = append(out, &eventpb.CollectorKlineAbsent{SubjectId: absence.SubjectID, Start: from.Unix(), End: to.Unix()})
	}
	return out, nil
}
