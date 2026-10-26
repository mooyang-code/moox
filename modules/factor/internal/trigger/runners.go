package trigger

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
)

var ErrRunnersClosed = errors.New("factor period runners are closed")

type runnerJob struct {
	ctx  context.Context
	run  func(context.Context) (pipeline.Outcome, error)
	done chan runnerResult
}

type runnerResult struct {
	outcome pipeline.Outcome
	err     error
}

type RunnerStatus struct {
	TaskID string
	Queued int
	Active bool
}

type taskRunner struct {
	taskID string
	jobs   chan runnerJob
	active bool
}

// TaskRunners serializes periods for each set while allowing independent sets to run
// concurrently. Each worker holds the shared catalog lock for the whole run.
type TaskRunners struct {
	mu      sync.Mutex
	runners map[string]*taskRunner
	locks   TaskLocks
	wg      sync.WaitGroup
	closed  bool
	stop    chan struct{}
}

func NewTaskRunners(locks TaskLocks) *TaskRunners {
	return &TaskRunners{runners: make(map[string]*taskRunner), locks: locks, stop: make(chan struct{})}
}

func (l *TaskRunners) Do(ctx context.Context, taskID string, run func(context.Context) (pipeline.Outcome, error)) (pipeline.Outcome, error) {
	if l == nil || run == nil {
		return pipeline.Outcome{}, fmt.Errorf("factor period runner and run function are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if taskID == "" {
		return pipeline.Outcome{}, fmt.Errorf("factor period runner task_id is required")
	}
	job := runnerJob{ctx: ctx, run: run, done: make(chan runnerResult, 1)}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return pipeline.Outcome{}, ErrRunnersClosed
	}
	runner := l.runners[taskID]
	if runner == nil {
		runner = &taskRunner{taskID: taskID, jobs: make(chan runnerJob, 128)}
		l.runners[taskID] = runner
		l.wg.Add(1)
		go l.runLoop(runner)
	}
	l.mu.Unlock()

	select {
	case runner.jobs <- job:
	case <-ctx.Done():
		return pipeline.Outcome{}, ctx.Err()
	case <-l.stop:
		return pipeline.Outcome{}, ErrRunnersClosed
	}
	select {
	case result := <-job.done:
		return result.outcome, result.err
	case <-ctx.Done():
		return pipeline.Outcome{}, ctx.Err()
	case <-l.stop:
		return pipeline.Outcome{}, ErrRunnersClosed
	}
}

func (l *TaskRunners) runLoop(runner *taskRunner) {
	defer l.wg.Done()
	for {
		select {
		case job := <-runner.jobs:
			l.runJob(runner.taskID, job)
		case <-l.stop:
			l.failQueued(runner)
			return
		}
	}
}

func (l *TaskRunners) runJob(taskID string, job runnerJob) {
	result := runnerResult{}
	if err := job.ctx.Err(); err != nil {
		result.err = err
		job.done <- result
		return
	}
	l.setActive(taskID, true)
	defer l.setActive(taskID, false)
	if l.locks != nil {
		unlock, err := l.locks.LockContext(job.ctx, taskID)
		if err != nil {
			result.err = err
			job.done <- result
			return
		}
		defer unlock()
	}
	result.outcome, result.err = job.run(job.ctx)
	job.done <- result
}

func (l *TaskRunners) failQueued(runner *taskRunner) {
	for {
		select {
		case job := <-runner.jobs:
			job.done <- runnerResult{err: ErrRunnersClosed}
		default:
			return
		}
	}
}

func (l *TaskRunners) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.stop)
	}
	l.mu.Unlock()
	l.wg.Wait()
}

func (l *TaskRunners) Status() []RunnerStatus {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	status := make([]RunnerStatus, 0, len(l.runners))
	for taskID, runner := range l.runners {
		status = append(status, RunnerStatus{TaskID: taskID, Queued: len(runner.jobs), Active: runner.active})
	}
	sort.Slice(status, func(i, j int) bool { return status[i].TaskID < status[j].TaskID })
	return status
}

func (l *TaskRunners) setActive(taskID string, active bool) {
	l.mu.Lock()
	if runner := l.runners[taskID]; runner != nil {
		runner.active = active
	}
	l.mu.Unlock()
}
