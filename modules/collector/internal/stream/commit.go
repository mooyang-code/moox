package stream

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
)

const (
	// commitRetryInterval 是提交失败后的重试间隔；commitStopBefore 是截止前停止重试的余量。
	commitRetryInterval = 300 * time.Millisecond
	commitStopBefore    = 200 * time.Millisecond
)

// PeriodCommitter 写一个数据集一个周期（marketfetch.KlineCommitter）。
type PeriodCommitter interface {
	CommitPeriod(context.Context, marketfetch.KlineBatch) (marketfetch.KlineCommitResult, error)
}

// commitJob 是一次周期提交；done 在提交成功或放弃后调用（在提交协程里，必须很快返回）。
type commitJob struct {
	committer PeriodCommitter
	batch     marketfetch.KlineBatch
	deadline  time.Time
	step      time.Duration
	done      func(commitOutcome)
	index     int
	// notBefore 是失败后下一次尝试的最早时刻；outcome 累计各次尝试。
	notBefore time.Time
	outcome   commitOutcome
}

type commitOutcome struct {
	result   marketfetch.KlineCommitResult
	err      error
	started  time.Time
	finished time.Time
	attempts int
}

type jobHeap []*commitJob

func (h jobHeap) Len() int { return len(h) }
func (h jobHeap) Less(i, j int) bool {
	if !h[i].deadline.Equal(h[j].deadline) {
		return h[i].deadline.Before(h[j].deadline)
	}
	return h[i].step < h[j].step
}
func (h jobHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].index, h[j].index = i, j }
func (h *jobHeap) Push(x any)   { job := x.(*commitJob); job.index = len(*h); *h = append(*h, job) }
func (h *jobHeap) Pop() any {
	old := *h
	job := old[len(old)-1]
	*h = old[:len(old)-1]
	return job
}

// CommitQueue 串行执行周期提交：截止早的先发（整点 1m 自然排在 1h 之前），失败按间隔重试到截止前。
// 闸门不阻塞在网络上，只把任务放进队列。
type CommitQueue struct {
	clock Clock
	mu    sync.Mutex
	jobs  jobHeap
	wake  chan struct{}
	// Timeout 限制单次提交的时长（默认到截止为止）。
	Timeout time.Duration
}

// NewCommitQueue 构造提交队列；Run 启动唯一的提交协程。
func NewCommitQueue(clock Clock) *CommitQueue {
	return &CommitQueue{clock: clock, wake: make(chan struct{}, 1)}
}

func (q *CommitQueue) submit(job *commitJob) {
	q.mu.Lock()
	heap.Push(&q.jobs, job)
	q.mu.Unlock()
	notify(q.wake)
}

// next 取出可以执行的任务：已到重试时刻的任务里截止最早的；都没到时返回最早的重试时刻。
func (q *CommitQueue) next(now time.Time) (*commitJob, time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var best *commitJob
	var wakeAt time.Time
	for _, job := range q.jobs {
		if job.notBefore.After(now) {
			if wakeAt.IsZero() || job.notBefore.Before(wakeAt) {
				wakeAt = job.notBefore
			}
			continue
		}
		if best == nil || q.jobs.Less(job.index, best.index) {
			best = job
		}
	}
	if best != nil {
		heap.Remove(&q.jobs, best.index)
	}
	return best, wakeAt
}

// Pending 返回排队中的任务数。
func (q *CommitQueue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.jobs.Len()
}

// Run 执行队列直到 ctx 结束。每次只做一次尝试：失败的任务按重试间隔放回队列重新排队，
// 期间截止更早的任务（例如整点 1h 提交失败时到达的 1m）先执行，不会被长截止任务的重试占住。
func (q *CommitQueue) Run(ctx context.Context) {
	for {
		job, wakeAt := q.next(q.clock.Now())
		if job == nil {
			var timer Stopper
			retry := make(chan struct{})
			if !wakeAt.IsZero() {
				timer = q.clock.AfterFunc(wakeAt.Sub(q.clock.Now()), func() { close(retry) })
			}
			select {
			case <-q.wake:
			case <-retry:
			case <-ctx.Done():
			}
			if timer != nil {
				timer.Stop()
			}
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if q.attempt(ctx, job) {
			job.outcome.finished = q.clock.Now()
			job.done(job.outcome)
			continue
		}
		job.notBefore = q.clock.Now().Add(commitRetryInterval)
		q.submit(job)
	}
}

// attempt 做一次提交，返回任务是否结束（成功、到截止、或不再有重试的时间）。
func (q *CommitQueue) attempt(ctx context.Context, job *commitJob) bool {
	if job.outcome.started.IsZero() {
		job.outcome.started = q.clock.Now()
	}
	remaining := job.deadline.Sub(q.clock.Now())
	if remaining <= 0 {
		if job.outcome.err == nil {
			job.outcome.err = context.DeadlineExceeded
		}
		return true
	}
	job.outcome.attempts++
	timeout := remaining
	if q.Timeout > 0 && q.Timeout < timeout {
		timeout = q.Timeout
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	job.outcome.result, job.outcome.err = job.committer.CommitPeriod(attemptCtx, job.batch)
	cancel()
	if job.outcome.err == nil || ctx.Err() != nil {
		return true
	}
	return !q.clock.Now().Add(commitRetryInterval).Before(job.deadline.Add(-commitStopBefore))
}

// sleepClock 按注入的时钟等待（假时钟由测试推进）。
func sleepClock(ctx context.Context, clock Clock, d time.Duration) bool {
	done := make(chan struct{})
	timer := clock.AfterFunc(d, func() { close(done) })
	select {
	case <-done:
		return true
	case <-ctx.Done():
		timer.Stop()
		return false
	}
}
