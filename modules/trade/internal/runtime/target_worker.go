package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var ErrTargetWorkerConfig = errors.New("trade runtime: TargetWorker is not configured")

type LogicalTargetStore interface {
	ListLogicalAccountTargets(
		context.Context,
		...string,
	) ([]store.LogicalAccountTargetRecord, error)
}

type TargetConverger interface {
	Converge(context.Context, string, string) (targetapp.Result, error)
}

type TargetRunMetrics interface {
	ObserveRun(stage, result, pipeline string, at time.Time) error
}

type TargetWorker struct {
	Store    LogicalTargetStore
	Executor TargetConverger
	Interval time.Duration
	Now      func() time.Time
	Gate     sync.Locker
	Metrics  TargetRunMetrics

	wakeOnce sync.Once
	wake     chan struct{}
}

func (w *TargetWorker) Wake() {
	if w == nil {
		return
	}
	w.initWake()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *TargetWorker) Run(ctx context.Context) error {
	if w == nil || w.Store == nil || w.Executor == nil {
		return ErrTargetWorkerConfig
	}
	w.initWake()
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	_ = w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.wake:
		case <-ticker.C:
		}
		_ = w.runOnce(ctx)
	}
}

func (w *TargetWorker) runOnce(ctx context.Context) error {
	if w.Gate != nil {
		w.Gate.Lock()
		defer w.Gate.Unlock()
	}
	records, err := w.Store.ListLogicalAccountTargets(
		ctx,
		targetapp.StatusPending,
		targetapp.StatusConverging,
		targetapp.StatusBlocked,
	)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	var runErrors []error
	for _, record := range records {
		result, convergeErr := w.Executor.Converge(
			ctx,
			record.SpaceID,
			record.LogicalAccountID,
		)
		if convergeErr != nil {
			runErrors = append(runErrors, convergeErr)
		}
		w.observe(result, convergeErr, now)
	}
	return errors.Join(runErrors...)
}

func (w *TargetWorker) observe(
	result targetapp.Result,
	err error,
	at time.Time,
) {
	if w.Metrics == nil {
		return
	}
	outcome := "running"
	switch {
	case err != nil:
		outcome = "error"
	case result.Status == targetapp.StatusConverged:
		outcome = "success"
	case result.Status == targetapp.StatusBlocked ||
		result.Status == targetapp.StatusPaused:
		outcome = "rejected"
	}
	_ = w.Metrics.ObserveRun("target", outcome, "trade-target", at)
}

func (w *TargetWorker) initWake() {
	w.wakeOnce.Do(func() {
		w.wake = make(chan struct{}, 1)
	})
}
