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

func (q *CommitQueue) next() *commitJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.jobs.Len() == 0 {
		return nil
	}
	return heap.Pop(&q.jobs).(*commitJob)
}

// Pending 返回排队中的任务数。
func (q *CommitQueue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.jobs.Len()
}

// Run 执行队列直到 ctx 结束。
func (q *CommitQueue) Run(ctx context.Context) {
	for {
		job := q.next()
		if job == nil {
			select {
			case <-q.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		job.done(q.execute(ctx, job))
	}
}

func (q *CommitQueue) execute(ctx context.Context, job *commitJob) commitOutcome {
	outcome := commitOutcome{started: q.clock.Now()}
	for {
		outcome.attempts++
		remaining := job.deadline.Sub(q.clock.Now())
		if remaining <= 0 {
			if outcome.err == nil {
				outcome.err = context.DeadlineExceeded
			}
			break
		}
		timeout := remaining
		if q.Timeout > 0 && q.Timeout < timeout {
			timeout = q.Timeout
		}
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		outcome.result, outcome.err = job.committer.CommitPeriod(attemptCtx, job.batch)
		cancel()
		if outcome.err == nil || ctx.Err() != nil {
			break
		}
		if !q.clock.Now().Add(commitRetryInterval).Before(job.deadline.Add(-commitStopBefore)) {
			break
		}
		if !sleepClock(ctx, q.clock, commitRetryInterval) {
			break
		}
	}
	outcome.finished = q.clock.Now()
	return outcome
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
