package trigger

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
)

var ErrLanesClosed = errors.New("factor period lanes are closed")

type laneJob struct {
	ctx  context.Context
	run  func(context.Context) (pipeline.Outcome, error)
	done chan laneResult
}

type laneResult struct {
	outcome pipeline.Outcome
	err     error
}

type periodLane struct {
	setID string
	jobs  chan laneJob
}

// Lanes serializes periods for each set while allowing independent sets to run
// concurrently. Each worker holds the shared catalog lock for the whole run.
type Lanes struct {
	mu     sync.Mutex
	lanes  map[string]*periodLane
	locks  SetLocks
	wg     sync.WaitGroup
	closed bool
	stop   chan struct{}
}

func NewLanes(locks SetLocks) *Lanes {
	return &Lanes{lanes: make(map[string]*periodLane), locks: locks, stop: make(chan struct{})}
}

func (l *Lanes) Do(ctx context.Context, setID string, run func(context.Context) (pipeline.Outcome, error)) (pipeline.Outcome, error) {
	if l == nil || run == nil {
		return pipeline.Outcome{}, fmt.Errorf("factor period lane and run function are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if setID == "" {
		return pipeline.Outcome{}, fmt.Errorf("factor period lane set_id is required")
	}
	job := laneJob{ctx: ctx, run: run, done: make(chan laneResult, 1)}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return pipeline.Outcome{}, ErrLanesClosed
	}
	lane := l.lanes[setID]
	if lane == nil {
		lane = &periodLane{setID: setID, jobs: make(chan laneJob, 128)}
		l.lanes[setID] = lane
		l.wg.Add(1)
		go l.runLane(lane)
	}
	l.mu.Unlock()

	select {
	case lane.jobs <- job:
	case <-ctx.Done():
		return pipeline.Outcome{}, ctx.Err()
	case <-l.stop:
		return pipeline.Outcome{}, ErrLanesClosed
	}
	select {
	case result := <-job.done:
		return result.outcome, result.err
	case <-ctx.Done():
		return pipeline.Outcome{}, ctx.Err()
	case <-l.stop:
		return pipeline.Outcome{}, ErrLanesClosed
	}
}

func (l *Lanes) runLane(lane *periodLane) {
	defer l.wg.Done()
	for {
		select {
		case job := <-lane.jobs:
			l.runJob(lane.setID, job)
		case <-l.stop:
			l.failQueued(lane)
			return
		}
	}
}

func (l *Lanes) runJob(setID string, job laneJob) {
	result := laneResult{}
	if err := job.ctx.Err(); err != nil {
		result.err = err
		job.done <- result
		return
	}
	if l.locks != nil {
		unlock := l.locks.Lock(setID)
		if unlock != nil {
			defer unlock()
		}
	}
	result.outcome, result.err = job.run(job.ctx)
	job.done <- result
}

func (l *Lanes) failQueued(lane *periodLane) {
	for {
		select {
		case job := <-lane.jobs:
			job.done <- laneResult{err: ErrLanesClosed}
		default:
			return
		}
	}
}

func (l *Lanes) Close() {
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
