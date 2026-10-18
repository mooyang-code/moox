package pipeline

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

// warmupRetryDelay spaces warm-up attempts after a read failure, so a Storage
// outage is not hammered while live periods keep re-queueing the subjects.
const warmupRetryDelay = 10 * time.Second

func (r *Runner) Load(ctx context.Context, plan Plan) (LoadResult, error) {
	if r == nil || r.store == nil {
		return LoadResult{}, errors.New("factor Storage store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := LoadResult{Frames: make(map[string]*storageio.Frame)}
	subjects := uniqueSorted(plan.Available)
	failedSet := make(map[string]struct{}, len(plan.UpstreamFailed))
	for _, subject := range plan.UpstreamFailed {
		failedSet[subject] = struct{}{}
	}
	if len(subjects) == 0 {
		result.FailedSubjects = uniqueSorted(plan.UpstreamFailed)
		return result, nil
	}
	if !plan.TargetStart.Before(plan.TargetEnd) {
		return LoadResult{}, errors.New("factor read window must have start before end")
	}
	live := plan.Mode == ModeLive && r.windows != nil
	var reads map[string]subjectRead
	if live {
		reads, result.WarmingSubjects = r.windows.plan(plan, subjects)
		for _, subject := range result.WarmingSubjects {
			failedSet[subject] = struct{}{}
		}
	} else {
		start, err := readWindowStart(r.clock, plan)
		if err != nil {
			return LoadResult{}, err
		}
		reads = make(map[string]subjectRead, len(subjects))
		for _, subject := range subjects {
			reads[subject] = subjectRead{start: start}
		}
	}

	frames, failed, err := r.readSubjects(ctx, plan, reads, plan.Mode == ModeRecalc, false)
	if err != nil {
		return LoadResult{}, err
	}
	for subject := range failed {
		failedSet[subject] = struct{}{}
	}
	for subject, frame := range frames {
		if prefix := reads[subject].prefix; len(prefix) > 0 {
			frame.Rows = append(append(make([][]any, 0, len(prefix)+len(frame.Rows)), prefix...), frame.Rows...)
		}
		result.Frames[subject] = frame
	}
	if live {
		r.storeWindows(ctx, plan, result.Frames)
	}
	for _, subject := range subjects {
		if _, failed := failedSet[subject]; !failed {
			result.Available = append(result.Available, subject)
		}
	}
	for subject := range failedSet {
		result.FailedSubjects = append(result.FailedSubjects, subject)
	}
	result.FailedSubjects = uniqueSorted(result.FailedSubjects)
	return result, nil
}

// readSubjects reads each subject from its own start to the plan's end.
// Subjects sharing a start share batches, read by a bounded worker pool. A
// failed batch marks its subjects failed; only when every batch fails is the
// read an infrastructure error.
func (r *Runner) readSubjects(ctx context.Context, plan Plan, reads map[string]subjectRead, pageDeadline, warmup bool) (map[string]*storageio.Frame, map[string]struct{}, error) {
	type readJob struct {
		start    time.Time
		subjects []string
	}
	batchSize, workerLimit, pause := r.cfg.ReadBatchSubjects, r.cfg.ReadWorkers, time.Duration(0)
	if warmup {
		// 首次预热允许慢：小批量、低并发、批间停顿，避免瞬时大量读请求压垮 Storage。
		batchSize, workerLimit, pause = r.cfg.WarmupBatchSubjects, r.cfg.WarmupWorkers, r.cfg.WarmupBatchPause
	}
	if batchSize < 1 {
		batchSize = 100
	}
	// 补算和预热整窗口读取：并发再按 BulkReadWorkers 收敛，实时读取不受影响。
	if pageDeadline && r.cfg.BulkReadWorkers > 0 {
		workerLimit = min(workerLimit, r.cfg.BulkReadWorkers)
	}
	groups := make(map[time.Time][]string)
	var starts []time.Time
	for _, subject := range uniqueSorted(keysOf(reads)) {
		start := reads[subject].start
		if _, seen := groups[start]; !seen {
			starts = append(starts, start)
		}
		groups[start] = append(groups[start], subject)
	}
	var jobs []readJob
	for _, start := range starts {
		group := groups[start]
		for from := 0; from < len(group); from += batchSize {
			to := min(from+batchSize, len(group))
			jobs = append(jobs, readJob{start: start, subjects: append([]string(nil), group[from:to]...)})
		}
	}
	frames := make(map[string]*storageio.Frame, len(reads))
	failed := make(map[string]struct{})
	if len(jobs) == 0 {
		return frames, failed, nil
	}
	workers := max(1, min(workerLimit, len(jobs)))
	// 只有批量读取才用断路：实时读取的批次少、要快速失败，连续失败也不能拖累下一期。
	breaker := &readBreaker{limit: 0}
	if pageDeadline {
		breaker.limit = int32(r.cfg.ReadBreakerFailures)
	}
	type batchResult struct {
		subjects []string
		frames   map[string]*storageio.Frame
		err      error
	}
	queue := make(chan readJob)
	results := make(chan batchResult, len(jobs))
	var workersDone sync.WaitGroup
	for i := 0; i < workers; i++ {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			for job := range queue {
				var batch map[string]*storageio.Frame
				var err error
				if breaker.open() {
					err = fmt.Errorf("%w: read circuit open after %d consecutive failed batches", storageio.ErrInfra, breaker.failures.Load())
				} else {
					batch, err = r.readBatch(ctx, plan, job.start, job.subjects, pageDeadline)
					breaker.record(err)
				}
				results <- batchResult{subjects: job.subjects, frames: batch, err: err}
				if pause > 0 {
					select {
					case <-time.After(pause):
					case <-ctx.Done():
					}
				}
			}
		}()
	}
	go func() {
		defer func() {
			close(queue)
			workersDone.Wait()
			close(results)
		}()
		for _, job := range jobs {
			select {
			case queue <- job:
			case <-ctx.Done():
				return
			}
		}
	}()

	failedBatches := 0
	var firstErr error
	for batch := range results {
		if batch.err != nil {
			failedBatches++
			if firstErr == nil {
				firstErr = batch.err
			}
			for _, subject := range batch.subjects {
				failed[subject] = struct{}{}
			}
			continue
		}
		for _, subject := range batch.subjects {
			frame := batch.frames[subject]
			if frame == nil {
				frame = &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, plan.CarryColumns...), Rows: [][]any{}}
			}
			frames[subject] = frame
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if failedBatches == len(jobs) {
		return nil, nil, fmt.Errorf("%w: all %d factor read batches failed: %v", storageio.ErrInfra, failedBatches, firstErr)
	}
	return frames, failed, nil
}

// readBatch reads one batch with retries. A live read covers a few bars and
// must fail fast, so the whole batch shares one deadline. A recalc or warm-up
// read spans the whole lookback over a link the read workers share; it is
// bounded per page instead, so a slow but progressing read is not abandoned.
func (r *Runner) readBatch(ctx context.Context, plan Plan, start time.Time, subjects []string, pageDeadline bool) (map[string]*storageio.Frame, error) {
	retries := max(r.cfg.ReadRetries, 0)
	timeout := r.cfg.ReadTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		readCtx, cancel := ctx, context.CancelFunc(func() {})
		pageTimeout := time.Duration(0)
		if pageDeadline {
			pageTimeout = timeout
		} else {
			readCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		attemptStart := time.Now()
		frames, err := r.store.ReadWindow(readCtx, storageio.ReadRequest{
			SpaceID: plan.Set.SpaceID, DatasetID: plan.Set.SourceDatasetID, Freq: plan.Set.Freq,
			Subjects: append([]string(nil), subjects...), Start: start, End: plan.TargetEnd,
			Columns: append([]string(nil), plan.CarryColumns...), PageTimeout: pageTimeout,
		})
		cancel()
		if err == nil {
			return frames, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A failed batch only surfaces as degraded subjects in the outcome;
		// log each attempt so the cause (timeout, Storage error) is visible.
		log.Warnf("factor_read_batch_failed set_id=%s subjects=%d first=%s attempt=%d/%d after=%s timeout=%s error=%v",
			plan.Set.SetID, len(subjects), subjects[0], attempt+1, retries+1, time.Since(attemptStart).Round(time.Millisecond), timeout, err)
		// 批量读取失败后先退避再重试：Storage 已经读不动时，立即重试只会让它更慢。实时读取不等待。
		if pageDeadline && attempt < retries {
			if waitErr := sleepContext(ctx, retryDelay(r.cfg.ReadRetryBackoff, attempt)); waitErr != nil {
				return nil, waitErr
			}
		}
	}
	return nil, lastErr
}

// maxReadRetryDelay 是两次重试之间等待的上限。
const maxReadRetryDelay = 30 * time.Second

// retryDelay 返回第 attempt（从 0 开始）次失败之后的等待：base 按 2 的幂递增，加减 20% 的抖动，不超过 maxReadRetryDelay。
func retryDelay(base time.Duration, attempt int) time.Duration {
	delay := base
	for i := 0; i < attempt && delay < maxReadRetryDelay; i++ {
		delay *= 2
	}
	delay = min(delay, maxReadRetryDelay)
	if jitter := int64(delay / 5); jitter > 0 {
		delay += time.Duration(rand.Int64N(2*jitter+1) - jitter)
	}
	return delay
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// readBreaker 统计一次批量读取里连续失败的批次数；达到 limit 后 open 为真，其余批次不再发请求。
type readBreaker struct {
	limit    int32
	failures atomic.Int32
}

func (b *readBreaker) open() bool { return b.limit > 0 && b.failures.Load() >= b.limit }

func (b *readBreaker) record(err error) {
	if b.limit <= 0 {
		return
	}
	if err != nil {
		b.failures.Add(1)
		return
	}
	b.failures.Store(0)
}

// RunWarmup loads the lookback window of the subjects live periods left out as
// warming, until ctx ends. It needs Config.BackgroundWarmup.
func (r *Runner) RunWarmup(ctx context.Context) {
	if r == nil || r.windows == nil || r.windows.wake == nil {
		return
	}
	for {
		plan, subjects, ok := r.windows.nextWarmup()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-r.windows.wake:
				continue
			}
		}
		if !r.warmup(ctx, plan, subjects) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(warmupRetryDelay):
			}
		}
	}
}

// Prewarm 为实时缓存里还没有的因子集排队后台预热 plan.Available 的回看窗口，返回排队的标的数。引擎在成为租约持有者、
// 或者目录里出现新的因子集之后调用它主动预热，不等第一个实时周期到达才开始：否则那一期的全部标的都在预热中，只能跳过。
// 已经有实时周期或预热记录的因子集不处理。plan 必须是实时计划，需要 Config.BackgroundWarmup。
func (r *Runner) Prewarm(plan Plan) int {
	if r == nil || r.windows == nil || plan.Mode != ModeLive || !plan.TargetStart.Before(plan.TargetEnd) {
		return 0
	}
	return r.windows.prewarm(plan, uniqueSorted(plan.Available))
}

// OnWarmupDone 登记一批后台预热载入窗口之后调用的函数（参数是集合），这时预热状态已经更新；在预热协程里同步调用、应当很快返回。
// 引擎用它让跳过恢复立刻补算预热期间跳过的那几期。
func (r *Runner) OnWarmupDone(hook func(setID string)) {
	if r == nil {
		return
	}
	if hook == nil {
		r.warmed.Store(nil)
		return
	}
	r.warmed.Store(&hook)
}

// WarmupStatuses reports the warm-up state of every live set seen so far.
func (r *Runner) WarmupStatuses() []WarmupStatus {
	if r == nil || r.windows == nil {
		return nil
	}
	return r.windows.statuses()
}

func (r *Runner) warmup(ctx context.Context, plan Plan, subjects []string) bool {
	started := time.Now()
	reads := make(map[string]subjectRead, len(subjects))
	for _, subject := range subjects {
		reads[subject] = subjectRead{start: plan.TargetStart}
	}
	frames, failed, err := r.readSubjects(ctx, plan, reads, true, true)
	if err != nil {
		r.windows.finishWarmup(plan, subjects)
		log.Warnf("factor_warmup_failed set_id=%s subjects=%d window=%s/%s duration=%s error=%v", plan.Set.SetID, len(subjects),
			plan.TargetStart.UTC().Format(time.RFC3339), plan.TargetEnd.UTC().Format(time.RFC3339), time.Since(started).Round(time.Millisecond), err)
		return false
	}
	// 写入缓存后紧接着结束这一批：两步之间实时周期已经能读到窗口，预热状态却还是 warming，间隔越短越好；日志与回调放在之后。
	r.storeWindows(ctx, plan, frames)
	r.windows.finishWarmup(plan, subjects)
	log.Infof("factor_warmup_done set_id=%s subjects=%d warmed=%d failed=%d window=%s/%s duration=%s", plan.Set.SetID, len(subjects), len(frames), len(failed),
		plan.TargetStart.UTC().Format(time.RFC3339), plan.TargetEnd.UTC().Format(time.RFC3339), time.Since(started).Round(time.Millisecond))
	if hook := r.warmed.Load(); hook != nil && len(frames) > 0 {
		(*hook)(plan.Set.SetID)
	}
	return len(failed) == 0
}

func readWindowStart(clock periodclock.Clock, plan Plan) (time.Time, error) {
	if plan.Mode != ModeRecalc || len(plan.Factors) == 0 {
		return plan.TargetStart, nil
	}
	maxLookback := 1
	for _, factor := range plan.Factors {
		if factor.LookbackPeriods > maxLookback {
			maxLookback = factor.LookbackPeriods
		}
	}
	window, err := clock.Window(plan.TargetStart, plan.Set.Freq, maxLookback)
	if err != nil {
		return time.Time{}, fmt.Errorf("compute recalc read lookback: %w", err)
	}
	return window[0], nil
}

func keysOf[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// storeWindows 把读到的窗口写进实时缓存：缓存里的窗口一定无洞。有洞的标的不写缓存，标记为 dirty，下期整窗口读取；
// 读到完整窗口的标的解除 dirty。所有写缓存的入口（实时周期与后台预热）都经过这里，所以在途的旧预热结果也只可能写入无洞窗口。
// 未开启空洞检查时与直接写缓存完全一致。
func (r *Runner) storeWindows(ctx context.Context, plan Plan, frames map[string]*storageio.Frame) {
	if !r.cfg.GapCheck {
		r.windows.store(plan, frames)
		return
	}
	holes := r.cacheHoles(ctx, plan, frames)
	clean := make(map[string]*storageio.Frame, len(frames))
	cleared := make([]string, 0, len(frames))
	for subject, frame := range frames {
		if _, bad := holes[subject]; bad {
			continue
		}
		clean[subject] = frame
		cleared = append(cleared, subject)
	}
	r.windows.store(plan, clean)
	r.windows.markDirty(plan, holes, cleared)
}
