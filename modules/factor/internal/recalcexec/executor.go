// Package recalcexec runs a recalc window in chunks through the factor
// pipeline. It is store-free: moox-factor-engine persists progress through the
// manager, the manager never executes jobs.
package recalcexec

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// Runner computes one pipeline plan.
type Runner interface {
	Run(context.Context, pipeline.Plan) (pipeline.Outcome, error)
}

// SetLocker serializes recalc chunks with live periods of the same set.
type SetLocker interface {
	LockContext(context.Context, string) (func(), error)
}

// Selection is the input of one recalc chunk: the set, the factors to compute,
// the resolved subjects and the source columns carried into the result.
type Selection struct {
	Set      domain.FactorSet
	Factors  []domain.FactorDef
	Subjects []string
	Columns  []string
}

// SelectionSource is consulted under the set lock before every chunk attempt,
// so a caller can re-read membership or observe a cancellation between chunks.
type SelectionSource func(context.Context) (Selection, error)

// ProgressFunc records a completed chunk. Returning stop=true ends the run
// without error, e.g. because the job was cancelled.
type ProgressFunc func(ctx context.Context, progress time.Time, outcome pipeline.Outcome) (stop bool, err error)

// Window is the aligned [Start, End) range of a job and the resume point.
type Window struct {
	Start    time.Time
	End      time.Time
	Progress time.Time
}

const defaultChunkPeriods = 2000

type config struct {
	chunkPeriods int
	chunkRetries int
	retryBackoff time.Duration
	clock        periodclock.Clock
	locks        SetLocker
}

type Option func(*config)

func WithChunkPeriods(n int) Option { return func(cfg *config) { cfg.chunkPeriods = n } }
func WithChunkRetry(attempts int, backoff time.Duration) Option {
	return func(cfg *config) { cfg.chunkRetries, cfg.retryBackoff = attempts, backoff }
}
func WithClock(clock periodclock.Clock) Option { return func(cfg *config) { cfg.clock = clock } }
func WithLocks(locks SetLocker) Option         { return func(cfg *config) { cfg.locks = locks } }

// Executor runs a recalc window in chunks without touching any job store; the
// caller persists progress through ProgressFunc.
type Executor struct {
	runner       Runner
	locks        SetLocker
	clock        periodclock.Clock
	chunkPeriods int
	chunkRetries int
	retryBackoff time.Duration
}

func NewExecutor(runner Runner, options ...Option) *Executor {
	cfg := config{chunkPeriods: defaultChunkPeriods, chunkRetries: 3, retryBackoff: 5 * time.Second}
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}
	if cfg.chunkPeriods <= 0 {
		cfg.chunkPeriods = defaultChunkPeriods
	}
	if cfg.chunkRetries <= 0 {
		cfg.chunkRetries = 1
	}
	return &Executor{
		runner: runner, locks: cfg.locks, clock: cfg.clock,
		chunkPeriods: cfg.chunkPeriods, chunkRetries: cfg.chunkRetries, retryBackoff: cfg.retryBackoff,
	}
}

// Run executes the window from its resume point. An empty subject selection
// completes the window at once. Every finished chunk is reported before the
// next one starts; the final report carries progress == window.End.
func (e *Executor) Run(ctx context.Context, window Window, source SelectionSource, report ProgressFunc) error {
	if e == nil || e.runner == nil {
		return errors.New("factor recalc runner is required")
	}
	if source == nil || report == nil {
		return errors.New("recalc selection source and progress reporter are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	progress := window.Progress
	if progress.Before(window.Start) {
		progress = window.Start
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !progress.Before(window.End) {
			_, err := report(ctx, window.End, pipeline.Outcome{})
			return err
		}
		chunkEnd, outcome, empty, err := e.runChunkWithRetry(ctx, source, progress, window.End)
		if err != nil {
			return err
		}
		if empty {
			_, err := report(ctx, window.End, pipeline.Outcome{})
			return err
		}
		stop, err := report(ctx, chunkEnd, outcome)
		if err != nil || stop || !chunkEnd.Before(window.End) {
			return err
		}
		progress = chunkEnd
	}
}

// RunChunk computes one explicit range with a fixed selection.
func (e *Executor) RunChunk(ctx context.Context, selection Selection, start, end time.Time) (pipeline.Outcome, error) {
	if e == nil || e.runner == nil {
		return pipeline.Outcome{}, errors.New("factor recalc runner is required")
	}
	unlock, err := e.lock(ctx, selection.Set.SetID)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	defer unlock()
	return e.runner.Run(ctx, plan(selection, start, end))
}

// runChunkWithRetry retries a chunk only for transient Storage failures. Progress
// is committed after a successful chunk, so a final failure still leaves the job
// resumable from the last completed chunk.
func (e *Executor) runChunkWithRetry(ctx context.Context, source SelectionSource, start, windowEnd time.Time) (time.Time, pipeline.Outcome, bool, error) {
	var lastErr error
	for attempt := 1; attempt <= e.chunkRetries; attempt++ {
		chunkEnd, outcome, empty, err := e.runChunk(ctx, source, start, windowEnd)
		if err == nil {
			return chunkEnd, outcome, empty, nil
		}
		lastErr = err
		if !errors.Is(err, storageio.ErrInfra) || attempt == e.chunkRetries {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * e.retryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return time.Time{}, pipeline.Outcome{}, false, ctx.Err()
		case <-timer.C:
		}
	}
	return time.Time{}, pipeline.Outcome{}, false, lastErr
}

func (e *Executor) runChunk(ctx context.Context, source SelectionSource, start, windowEnd time.Time) (time.Time, pipeline.Outcome, bool, error) {
	selection, err := source(ctx)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	if len(selection.Subjects) == 0 {
		return windowEnd, pipeline.Outcome{}, true, nil
	}
	chunkEnd, err := e.chunkEnd(selection.Set, start, windowEnd)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	unlock, err := e.lock(ctx, selection.Set.SetID)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	// The selection may have changed while waiting for the lock (a member was
	// disabled or the job cancelled), so it is consulted again under the lock.
	selection, err = source(ctx)
	if err != nil {
		return time.Time{}, pipeline.Outcome{}, false, err
	}
	outcome, err := e.runner.Run(ctx, plan(selection, start, chunkEnd))
	return chunkEnd, outcome, false, err
}

func (e *Executor) chunkEnd(set domain.FactorSet, start, windowEnd time.Time) (time.Time, error) {
	clock := e.clock
	if clock == nil {
		var err error
		if clock, err = periodclock.ForSpace(set.SpaceID); err != nil {
			return time.Time{}, err
		}
	}
	duration, err := clock.Duration(set.Freq)
	if err != nil {
		return time.Time{}, err
	}
	if int64(e.chunkPeriods) > math.MaxInt64/int64(duration) {
		return time.Time{}, errors.New("recalc chunk duration overflows")
	}
	end := start.Add(time.Duration(e.chunkPeriods) * duration)
	if end.After(windowEnd) {
		end = windowEnd
	}
	return end, nil
}

func (e *Executor) lock(ctx context.Context, setID string) (func(), error) {
	if e.locks == nil {
		return func() {}, nil
	}
	unlock, err := e.locks.LockContext(ctx, setID)
	if err != nil {
		return nil, fmt.Errorf("lock factor set %s: %w", setID, err)
	}
	return unlock, nil
}

func plan(selection Selection, start, end time.Time) pipeline.Plan {
	return pipeline.Plan{
		Mode: pipeline.ModeRecalc, Set: selection.Set, Factors: selection.Factors,
		TargetStart: start, TargetEnd: end,
		Expected:     append([]string(nil), selection.Subjects...),
		Available:    append([]string(nil), selection.Subjects...),
		CarryColumns: selection.Columns, WriteCarry: true,
	}
}

const degradedPrefix = "degraded:"

// DegradedNote summarises partial failures of a chunk; the job still succeeds
// because the factor values of unaffected subjects were written.
func DegradedNote(outcome pipeline.Outcome) string {
	if outcome.Status != "degraded" {
		return ""
	}
	var factors []string
	for _, factor := range outcome.Factors {
		if factor.Status != "complete" {
			factors = append(factors, factor.FactorID)
		}
	}
	note := fmt.Sprintf("%s failed_subjects=%d", degradedPrefix, len(outcome.FailedSubjects))
	if len(factors) > 0 {
		note += " factors=" + strings.Join(factors, ",")
	}
	return note
}
