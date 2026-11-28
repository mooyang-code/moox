package resample

import (
	"context"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// TaskIndex 按（空间、源数据集、源频率）找出就绪的合成任务；任务清单按 TTL 缓存，任务变化最多延迟一个 TTL 生效。
type TaskIndex struct {
	Tasks  *store.TaskRepository
	Source subjectSource
	TTL    time.Duration

	mu      sync.Mutex
	cached  map[string]cachedTasks
	nowFunc func() time.Time
}

type cachedTasks struct {
	at    time.Time
	tasks []*Task
}

func (i *TaskIndex) now() time.Time {
	if i.nowFunc != nil {
		return i.nowFunc()
	}
	return time.Now()
}

// Match 返回源数据集与源频率匹配的任务。
func (i *TaskIndex) Match(ctx context.Context, spaceID, datasetID, frequency string) ([]*Task, error) {
	tasks, err := i.space(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	var out []*Task
	for _, task := range tasks {
		if task.Spec.SourceDatasetID == datasetID && task.Source.Storage == frequency {
			out = append(out, task)
		}
	}
	return out, nil
}

func (i *TaskIndex) space(ctx context.Context, spaceID string) ([]*Task, error) {
	ttl := i.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if entry, ok := i.cached[spaceID]; ok && i.now().Sub(entry.at) < ttl {
		return entry.tasks, nil
	}
	collections, err := i.Tasks.ListEnabled(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	var tasks []*Task
	for _, collection := range collections {
		if !strings.EqualFold(collection.DataType, "kline_resample") || collection.PrepareState != domain.PrepareStateReady {
			continue
		}
		if i.Source == nil {
			return nil, fmt.Errorf("resample task index source is required")
		}
		params, parseErr := domain.ParseCollectParams(collection.CollectParams, collection.DataType)
		if parseErr != nil {
			log.WarnContextf(ctx, "[Collector] resample task %s has invalid params, skipped: %v", collection.TaskID, parseErr)
			continue
		}
		sourceInfo, sourceErr := i.Source.GetDataset(ctx, collection.SpaceID, params.SourceDatasetID)
		if sourceErr != nil {
			// 一个任务的源数据集读不到（例如已删但任务仍启用）只跳过它自己，不能让同空间其余合成任务的事件一起失败；
			// 临时读取失败的任务在缓存过期（一个 TTL）后重新加载，期间由每分钟兜底覆盖。
			log.WarnContextf(ctx, "[Collector] resample task %s source dataset %s/%s is unavailable, skipped: %v", collection.TaskID, collection.SpaceID, params.SourceDatasetID, sourceErr)
			continue
		}
		task, err := NewTaskForSource(collection, sourceInfo)
		if err != nil {
			log.WarnContextf(ctx, "[Collector] resample task %s is invalid, skipped: %v", collection.TaskID, err)
			continue
		}
		tasks = append(tasks, task)
	}
	if i.cached == nil {
		i.cached = map[string]cachedTasks{}
	}
	i.cached[spaceID] = cachedTasks{at: i.now(), tasks: tasks}
	return tasks, nil
}

// PeriodTrigger 处理源数据集的 DatasetPeriodCompleted：收到桶内最后一个源周期的完成事件，就对该桶按数据集批量合成。
// 传输层（JetStream 解码、确认与重试）在 eventconsumer 子包里，这里只做领域判断；返回错误表示需要重试。
type PeriodTrigger struct {
	Index   *TaskIndex
	Engine  *Engine
	Metrics *Metrics
}

// OnPeriod 处理一个源周期完成事件。
func (t *PeriodTrigger) OnPeriod(ctx context.Context, spaceID string, payload *eventpb.DatasetPeriodCompleted) error {
	if t.Metrics != nil {
		t.Metrics.Events.Inc()
	}
	tasks, err := t.Index.Match(ctx, spaceID, payload.GetDatasetId(), payload.GetFrequency())
	if err != nil {
		return err
	}
	period := time.Unix(payload.GetPeriodTime(), 0).UTC()
	var errs []error
	for _, task := range tasks {
		bucket, ok, err := task.Clock.BucketAt(period)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// 只有桶内最后一个源周期收盘时，桶才收盘；其余源周期的事件不触发。
		if !ok || !bucket.End.Equal(period.Add(task.Source.Duration)) {
			continue
		}
		if _, err := t.Engine.RunBucket(ctx, task, bucket, periodSubjects(payload), ""); err != nil {
			errs = append(errs, fmt.Errorf("task %s: %w", task.Spec.TaskID, err))
		}
	}
	return errors.Join(errs...)
}

// periodSubjects 由周期完成事件算出可以合成的标的：全集减去失败的标的。不按 tag 过滤：源任务换过主来源、tag 变了的标的
// 照样交给 RunBucket，它按任务的 SourceSeriesTag 读源行，读不到就按“源窗口不完整”跳过（不写、不等待）。
func periodSubjects(payload *eventpb.DatasetPeriodCompleted) []string {
	failed := make(map[string]struct{}, len(payload.GetFailedSubjects()))
	for _, subject := range payload.GetFailedSubjects() {
		failed[subject] = struct{}{}
	}
	var subjects []string
	for _, subject := range payload.GetUniverseSubjectIds() {
		if _, bad := failed[subject]; !bad {
			subjects = append(subjects, subject)
		}
	}
	return uniqueSorted(subjects)
}

// BackfillTrigger 处理 event.collector.kline.backfilled：源数据被补写后，对依赖它的合成任务提交历史合成作业。
type BackfillTrigger struct {
	Index *TaskIndex
	State *store.ResampleStateRepository
	// SettleAfter 是作业最早开始时间相对事件确认时刻的推迟量（2×单次执行硬超时），
	// 保证补采写入之前读过旧源数据的执行都已写完或放弃，不会用旧结果盖掉新结果。
	SettleAfter time.Duration
	NewID       func() string
	Now         func() time.Time
}

// OnBackfilled 处理一个补采完成事件；eventID 作为作业的 request_id，重复投递不会重复建作业。
func (t *BackfillTrigger) OnBackfilled(ctx context.Context, spaceID, eventID string, payload *eventpb.CollectorKlineBackfilled) error {
	tasks, err := t.Index.Match(ctx, spaceID, payload.GetDatasetId(), payload.GetFrequency())
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if t.Now != nil {
		now = t.Now().UTC()
	}
	newID := t.NewID
	if newID == nil {
		newID = func() string { return fmt.Sprintf("rsj-%d", time.Now().UnixNano()) }
	}
	var errs []error
	for _, task := range tasks {
		_, _, err := SubmitJob(ctx, t.State, task, SubmitJobRequest{
			SpaceID: task.Spec.SpaceID, TaskID: task.Spec.TaskID, RequestID: eventID, Origin: store.ResampleOriginSourceBackfill,
			Subjects: payload.GetSubjectIds(), Start: time.Unix(payload.GetStart(), 0).UTC(), End: time.Unix(payload.GetEnd(), 0).UTC(), NotBefore: time.UnixMilli(payload.GetConfirmedAt()).UTC().Add(t.SettleAfter),
		}, newID, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("task %s: %w", task.Spec.TaskID, err))
		}
	}
	return errors.Join(errs...)
}
