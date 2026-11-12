package resample

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// MaxJobBuckets 是单个历史合成作业最多覆盖的目标桶数。
const MaxJobBuckets = domain.MaxResampleBackfillBuckets

// maxJobFailures 是作业连续失败多少轮后标为失败。
const maxJobFailures = 5

// JobRunnerConfig 是历史合成作业的运行参数。
type JobRunnerConfig struct {
	// BucketsPerRound 是每个作业每轮最多推进的目标桶数，保证单轮有界。
	BucketsPerRound int
	// MaxJobsPerRound 是每轮最多推进的作业数（每个任务一个）。
	MaxJobsPerRound int
	// SyncTimeout 是作业结束时等待目标 View 跟上写入的时间。
	SyncTimeout time.Duration
}

func (c JobRunnerConfig) withDefaults() JobRunnerConfig {
	if c.BucketsPerRound <= 0 {
		c.BucketsPerRound = 20
	}
	if c.MaxJobsPerRound <= 0 {
		c.MaxJobsPerRound = 8
	}
	if c.SyncTimeout <= 0 {
		c.SyncTimeout = 5 * time.Second
	}
	return c
}

// JobRunner 推进历史合成作业：范围内的目标桶按游标逐桶执行，进程重启后从游标继续。
// 它与事件主路径、兜底检查互不加锁，三者写同一个桶时写出的是同一个值。
type JobRunner struct {
	State  *store.ResampleStateRepository
	Tasks  *store.TaskRepository
	Source subjectSource
	Engine *Engine
	// View 非空时，作业结束前先等目标 View 跟上写入，作业成功即 View 可读。
	View ViewWaiter
	// Flush 非空时，作业结束时立即冲刷它的补写通知（不等发布协程的下一轮）。
	Flush  func(ctx context.Context) error
	Config JobRunnerConfig

	mu       sync.Mutex
	failures map[string]int
}

// Tick 推进一轮。上一轮还没结束时直接跳过。
func (r *JobRunner) Tick(ctx context.Context, now time.Time) error {
	_, err := r.tick(ctx, now)
	return err
}

// Drain 连续推进，直到没有可运行的作业或用完预算：大范围的作业不必每分钟只走一轮。
func (r *JobRunner) Drain(ctx context.Context, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var firstErr error
	for ctx.Err() == nil && time.Now().Before(deadline) {
		advanced, err := r.tick(ctx, time.Now().UTC())
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if advanced == 0 || err != nil {
			break
		}
	}
	return firstErr
}

func (r *JobRunner) tick(ctx context.Context, now time.Time) (int, error) {
	if r == nil || r.State == nil || r.Tasks == nil || r.Source == nil || r.Engine == nil {
		return 0, errors.New("resample job runner dependencies are required")
	}
	if !r.mu.TryLock() {
		return 0, nil
	}
	defer r.mu.Unlock()
	cfg := r.Config.withDefaults()
	jobs, err := r.State.RunnableJobs(ctx, now, 0)
	if err != nil {
		return 0, err
	}
	var firstErr error
	advanced := 0
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		if advanced >= cfg.MaxJobsPerRound {
			break
		}
		moved, err := r.advance(ctx, job, now, cfg)
		if moved {
			advanced++
		}
		if err != nil {
			log.WarnContextf(ctx, "[Collector] resample job failed round job=%s task=%s: %v", job.JobID, job.TaskID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return advanced, firstErr
}

// advance 推进一个作业；moved 表示这一轮确实做了事（停用的任务、还没到开始时间的作业不算，不占每轮的作业名额）。
func (r *JobRunner) advance(ctx context.Context, job store.ResampleJob, now time.Time, cfg JobRunnerConfig) (moved bool, err error) {
	err = r.advanceJob(ctx, job, now, cfg, &moved)
	return moved, err
}

func (r *JobRunner) advanceJob(ctx context.Context, job store.ResampleJob, now time.Time, cfg JobRunnerConfig, moved *bool) error {
	collection, err := r.Tasks.GetByTaskID(ctx, job.SpaceID, job.TaskID)
	if err != nil || collection == nil {
		return r.fail(ctx, job, fmt.Errorf("task not found: %v", err), true)
	}
	if !collection.Enabled || collection.PrepareState != domain.PrepareStateReady {
		return nil // 任务停用或还没就绪：作业原地等待
	}
	*moved = true
	task, err := NewTask(*collection)
	if err != nil {
		return r.fail(ctx, job, err, true)
	}
	if job.Status == store.ResampleJobQueued {
		fresh, started, err := r.State.StartJob(ctx, job.JobID, now)
		if err != nil || !started {
			return err // 已被取消或最早开始时间被合并推后：下一轮再看
		}
		job = fresh
		end, err := r.effectiveEnd(task, job, now)
		if err != nil {
			return r.fail(ctx, job, err, false)
		}
		total, countErr := r.countBuckets(task, job.Start, end)
		if countErr != nil {
			return r.fail(ctx, job, countErr, false)
		}
		if total > MaxJobBuckets {
			return r.fail(ctx, job, fmt.Errorf("job covers %d buckets, more than the limit %d", total, MaxJobBuckets), true)
		}
		if err := r.State.SetJobTotal(ctx, job.JobID, total); err != nil {
			return err
		}
	}
	end, err := r.effectiveEnd(task, job, now)
	if err != nil {
		return r.fail(ctx, job, err, false)
	}
	if job.Status == store.ResampleJobSyncing {
		return r.finish(ctx, job, task, cfg)
	}

	buckets, err := task.Clock.Between(job.NextBucket, end, cfg.BucketsPerRound)
	if err != nil {
		return r.fail(ctx, job, err, false)
	}
	if len(buckets) == 0 {
		if err := r.State.SetJobStatus(ctx, job.JobID, store.ResampleJobSyncing, ""); err != nil {
			return err
		}
		job.Status = store.ResampleJobSyncing
		return r.finish(ctx, job, task, cfg)
	}
	members, err := r.jobMembers(ctx, task, job)
	if err != nil {
		return r.fail(ctx, job, err, false)
	}
	progress := store.JobProgress{NextBucket: job.NextBucket}
	for _, bucket := range buckets {
		if ctx.Err() != nil {
			break
		}
		// 每个桶开始前确认作业还在运行：取消之后不再继续写。
		if current, getErr := r.State.GetJob(ctx, job.SpaceID, job.JobID); getErr == nil && current.Status != store.ResampleJobRunning {
			return nil
		}
		result, runErr := r.Engine.RunBucketMembers(ctx, task, bucket, members, job.JobID)
		if runErr != nil {
			if progressErr := r.State.AdvanceJob(ctx, job.JobID, progress); progressErr != nil {
				log.WarnContextf(ctx, "[Collector] save resample job progress failed job=%s: %v", job.JobID, progressErr)
			}
			return r.fail(ctx, job, runErr, false)
		}
		progress.NextBucket = bucket.End
		progress.DoneBuckets++
		progress.Written += int64(result.Written)
		progress.Skipped += int64(len(result.Skipped))
		for _, skipped := range result.Skipped {
			if len(progress.SkippedSamples) < 5 {
				who := skipped.SubjectID
				if skipped.SeriesTag != "" {
					who += "[" + skipped.SeriesTag + "]"
				}
				progress.SkippedSamples = append(progress.SkippedSamples, fmt.Sprintf("%s@%s：%s", who, bucket.Key.Format(time.RFC3339), skipped.Reason))
			}
		}
	}
	r.clearFailures(job.JobID)
	if err := r.State.AdvanceJob(ctx, job.JobID, progress); err != nil {
		return err
	}
	if !progress.NextBucket.Before(end) {
		if err := r.State.SetJobStatus(ctx, job.JobID, store.ResampleJobSyncing, ""); err != nil {
			return err
		}
	}
	return nil
}

// effectiveEnd 把作业终点截到最近一根已收盘的桶，避免合成还在形成的桶。
func (r *JobRunner) effectiveEnd(task *Task, job store.ResampleJob, now time.Time) (time.Time, error) {
	latest, err := task.Clock.LatestClosed(now)
	if err != nil {
		return time.Time{}, err
	}
	if job.End.Before(latest.End) {
		return job.End, nil
	}
	return latest.End, nil
}

func (r *JobRunner) countBuckets(task *Task, start, end time.Time) (int, error) {
	if !end.After(start) {
		return 0, nil
	}
	buckets, err := task.Clock.Between(start, end, MaxJobBuckets+1)
	return len(buckets), err
}

// jobMembers 返回作业要合成的序列：作业指定了标的就取它与源数据集各序列成员的交集，否则取全部成员；
// 作业指定了源序列（补采只写了其中几条）就只取这些序列。
func (r *JobRunner) jobMembers(ctx context.Context, task *Task, job store.ResampleJob) (Members, error) {
	all, err := SourceMembers(ctx, r.Source, task)
	if err != nil {
		return nil, err
	}
	var tags map[string]struct{}
	if len(job.SeriesTags) > 0 {
		tags = make(map[string]struct{}, len(job.SeriesTags))
		for _, tag := range job.SeriesTags {
			tags[tag] = struct{}{}
		}
	}
	var wanted map[string]struct{}
	if len(job.Subjects) > 0 {
		wanted = make(map[string]struct{}, len(job.Subjects))
		for _, subject := range job.Subjects {
			wanted[subject] = struct{}{}
		}
	}
	out := Members{}
	for tag, subjects := range all {
		if tags != nil {
			if _, ok := tags[tag]; !ok {
				continue
			}
		}
		for _, subject := range subjects {
			if wanted != nil {
				if _, ok := wanted[subject]; !ok {
					continue
				}
			}
			out[tag] = append(out[tag], subject)
		}
	}
	return out, nil
}

// finish 冲刷作业的补写通知，等目标 View 跟上写入，然后标记成功。
func (r *JobRunner) finish(ctx context.Context, job store.ResampleJob, task *Task, cfg JobRunnerConfig) error {
	if r.Flush != nil {
		if err := r.Flush(ctx); err != nil {
			return r.fail(ctx, job, fmt.Errorf("flush backfill notices: %w", err), false)
		}
	}
	if r.View != nil {
		ids := taskresult.PersistedResultIDs(task.Spec.SpaceID, task.Spec.TaskID, task.Collection.ResultDatasetID, task.Collection.ResultViewID)
		if err := waitViewApplied(ctx, r.View, task.Spec.SpaceID, ids.ViewID, cfg.SyncTimeout); err != nil {
			return r.fail(ctx, job, err, false)
		}
	}
	r.clearFailures(job.JobID)
	return r.State.SetJobStatus(ctx, job.JobID, store.ResampleJobSucceeded, "")
}

// fail 记录一次失败；permanent 或连续失败达到上限时把作业标为失败，否则保留状态下一轮重试。
func (r *JobRunner) fail(ctx context.Context, job store.ResampleJob, cause error, permanent bool) error {
	r.failures = ensureMap(r.failures)
	r.failures[job.JobID]++
	if permanent || r.failures[job.JobID] >= maxJobFailures {
		delete(r.failures, job.JobID)
		if err := r.State.SetJobStatus(ctx, job.JobID, store.ResampleJobFailed, trimError(cause)); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	_ = r.State.SetJobStatus(ctx, job.JobID, job.Status, trimError(cause))
	return cause
}

func (r *JobRunner) clearFailures(jobID string) {
	if r.failures != nil {
		delete(r.failures, jobID)
	}
}

func ensureMap(m map[string]int) map[string]int {
	if m == nil {
		return map[string]int{}
	}
	return m
}

func trimError(err error) string {
	text := strings.TrimSpace(err.Error())
	if len(text) > 500 {
		return text[:500]
	}
	return text
}

// SubmitJobRequest 是提交历史合成作业的参数。
type SubmitJobRequest struct {
	SpaceID   string
	TaskID    string
	RequestID string
	Origin    string
	Subjects  []string
	// SeriesTags 限定要合成的源序列；为空表示任务的全部源序列（单序列任务恒为空）。
	SeriesTags []string
	Start      time.Time
	End        time.Time
	// NotBefore 是最早开始时间；为零表示立即。
	NotBefore time.Time
}

// SubmitJob 校验并提交历史合成作业：范围按 UTC 向外对齐到目标桶，最多 MaxJobBuckets 个桶。已存在同 request_id 的作业时原样返回。
func SubmitJob(ctx context.Context, state *store.ResampleStateRepository, task *Task, req SubmitJobRequest, newID func() string, now time.Time) (store.ResampleJob, bool, error) {
	if state == nil {
		return store.ResampleJob{}, false, errors.New("resample state repository is required")
	}
	if !req.End.After(req.Start) {
		return store.ResampleJob{}, false, errors.New("end must be after start")
	}
	aligned, err := task.Clock.Between(req.Start, req.End, MaxJobBuckets+1)
	if err != nil {
		return store.ResampleJob{}, false, err
	}
	if len(aligned) == 0 {
		return store.ResampleJob{}, false, errors.New("the range contains no target bucket")
	}
	if len(aligned) > MaxJobBuckets {
		return store.ResampleJob{}, false, fmt.Errorf("the range covers more than %d target buckets", MaxJobBuckets)
	}
	origin := req.Origin
	if origin == "" {
		origin = store.ResampleOriginManual
	}
	notBefore := req.NotBefore
	if notBefore.IsZero() {
		notBefore = now
	}
	return state.SubmitJob(ctx, store.ResampleJob{
		JobID: newID(), SpaceID: req.SpaceID, TaskID: req.TaskID, RequestID: req.RequestID, Origin: origin, Subjects: req.Subjects, SeriesTags: req.SeriesTags,
		Start: aligned[0].Start, End: aligned[len(aligned)-1].End, NotBefore: notBefore.UTC(), TotalBuckets: len(aligned),
	})
}
