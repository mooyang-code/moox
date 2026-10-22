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
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
	// SyncTimeout 是作业结束时等待 View 追上同步点的时间。
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
	// Sync 非空时，作业结束前先对目标数据集追加同步点并等 View 追上，作业成功即 View 可读。
	Sync SyncPointStorage
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
	jobs, err := r.State.RunnableJobs(ctx, now, cfg.MaxJobsPerRound)
	if err != nil {
		return 0, err
	}
	var firstErr error
	advanced := 0
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		advanced++
		if err := r.advance(ctx, job, now, cfg); err != nil {
			log.WarnContextf(ctx, "[Collector] resample job failed round job=%s task=%s: %v", job.JobID, job.TaskID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return advanced, firstErr
}

func (r *JobRunner) advance(ctx context.Context, job store.ResampleJob, now time.Time, cfg JobRunnerConfig) error {
	collection, err := r.Tasks.GetByTaskID(ctx, job.SpaceID, job.TaskID)
	if err != nil || collection == nil {
		return r.fail(ctx, job, fmt.Errorf("task not found: %v", err), true)
	}
	if !collection.Enabled || collection.PrepareState != domain.PrepareStateReady {
		return nil // 任务停用或还没就绪：作业原地等待
	}
	task, err := NewTask(*collection)
	if err != nil {
		return r.fail(ctx, job, err, true)
	}
	end, err := r.effectiveEnd(task, job, now)
	if err != nil {
		return r.fail(ctx, job, err, false)
	}
	if job.Status == store.ResampleJobQueued {
		total, countErr := r.countBuckets(task, job.Start, end)
		if countErr != nil {
			return r.fail(ctx, job, countErr, false)
		}
		if total > MaxJobBuckets {
			return r.fail(ctx, job, fmt.Errorf("job covers %d buckets, more than the limit %d", total, MaxJobBuckets), true)
		}
		if err := r.State.StartJob(ctx, job.JobID, total); err != nil {
			return err
		}
		job.Status = store.ResampleJobRunning
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
	subjects, err := r.jobSubjects(ctx, task, job)
	if err != nil {
		return r.fail(ctx, job, err, false)
	}
	progress := store.JobProgress{NextBucket: job.NextBucket}
	for _, bucket := range buckets {
		if ctx.Err() != nil {
			break
		}
		result, runErr := r.Engine.RunBucket(ctx, task, bucket, subjects, job.JobID)
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
				progress.SkippedSamples = append(progress.SkippedSamples, fmt.Sprintf("%s@%s：%s", skipped.SubjectID, bucket.Key.Format(time.RFC3339), skipped.Reason))
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

// jobSubjects 返回作业要合成的标的：作业指定了就取它与源数据集标的的交集，否则取源数据集的全部标的。
func (r *JobRunner) jobSubjects(ctx context.Context, task *Task, job store.ResampleJob) ([]string, error) {
	all, err := SourceSubjects(ctx, r.Source, task)
	if err != nil {
		return nil, err
	}
	if len(job.Subjects) == 0 {
		return all, nil
	}
	allowed := make(map[string]struct{}, len(all))
	for _, subject := range all {
		allowed[subject] = struct{}{}
	}
	out := make([]string, 0, len(job.Subjects))
	for _, subject := range job.Subjects {
		if _, ok := allowed[subject]; ok {
			out = append(out, subject)
		}
	}
	return out, nil
}

// finish 冲刷作业的补写通知，追加同步点并等目标 View 追上，然后标记成功。
func (r *JobRunner) finish(ctx context.Context, job store.ResampleJob, task *Task, cfg JobRunnerConfig) error {
	if r.Flush != nil {
		if err := r.Flush(ctx); err != nil {
			return r.fail(ctx, job, fmt.Errorf("flush backfill notices: %w", err), false)
		}
	}
	if r.Sync != nil {
		if err := r.waitViewSync(ctx, task, job.JobID, cfg.SyncTimeout); err != nil {
			return r.fail(ctx, job, err, false)
		}
	}
	r.clearFailures(job.JobID)
	return r.State.SetJobStatus(ctx, job.JobID, store.ResampleJobSucceeded, "")
}

// waitViewSync 对目标数据集追加同步点并等默认 View 追上：同步点和行写在同一个有序 outbox 里，View 处理到它，之前的行都已入索引。
func (r *JobRunner) waitViewSync(ctx context.Context, task *Task, requestID string, timeout time.Duration) error {
	ids := taskresult.PersistedResultIDs(task.Spec.SpaceID, task.Spec.TaskID, task.Collection.ResultDatasetID, task.Collection.ResultViewID)
	if err := r.Sync.AppendDatasetSyncPoint(ctx, task.Spec.SpaceID, task.Spec.TargetDatasetID, requestID, "catchup"); err != nil {
		return fmt.Errorf("append resample sync point: %w", err)
	}
	response, err := r.Sync.WaitViewSyncPoint(ctx, &storagepb.WaitViewSyncPointReq{
		SpaceId: task.Spec.SpaceID, ViewId: ids.ViewID, RequestId: requestID,
		DatasetIds: []string{task.Spec.TargetDatasetID}, WaitTimeoutMs: uint32(timeout / time.Millisecond),
	})
	if err != nil {
		return fmt.Errorf("wait resample View sync point: %w", err)
	}
	if response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("resample View sync point rejected")
	}
	if !response.GetReady() {
		return fmt.Errorf("resample View sync point is not ready")
	}
	return nil
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
	Start     time.Time
	End       time.Time
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
		JobID: newID(), SpaceID: req.SpaceID, TaskID: req.TaskID, RequestID: req.RequestID, Origin: origin, Subjects: req.Subjects,
		Start: aligned[0].Start, End: aligned[len(aligned)-1].End, NotBefore: notBefore.UTC(), TotalBuckets: len(aligned),
	})
}
