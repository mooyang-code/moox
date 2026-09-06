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
	ObserveRun(stage, result, healthCheck string, at time.Time) error
}

type TargetWorker struct {
	Store           LogicalTargetStore
	Executor        TargetConverger
	Interval        time.Duration
	Now             func() time.Time
	ConvergeTimeout time.Duration
	Metrics         TargetRunMetrics

	wakeOnce sync.Once
	wake     chan struct{}

	mu           sync.RWMutex
	ready        bool
	lastError    string
	targetErrors []TargetFailure
}

type TargetFailure struct {
	SpaceID          string `json:"space_id"`
	LogicalAccountID string `json:"logical_account_id"`
	TargetID         string `json:"target_id"`
	TradingAccountID string `json:"trading_account_id"`
	Error            string `json:"error"`
}

type TargetWorkerSnapshot struct {
	Ready        bool
	LastError    string
	TargetErrors []TargetFailure
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
	defer func() { w.setResult(context.Canceled) }()
	w.initWake()
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w.setResult(w.runOnce(ctx))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.wake:
		case <-ticker.C:
		}
		w.setResult(w.runOnce(ctx))
	}
}

func (w *TargetWorker) Snapshot() TargetWorkerSnapshot {
	if w == nil {
		return TargetWorkerSnapshot{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return TargetWorkerSnapshot{
		Ready: w.ready, LastError: w.lastError,
		TargetErrors: append([]TargetFailure(nil), w.targetErrors...),
	}
}

func (w *TargetWorker) setResult(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready = err == nil
	w.lastError = ""
	if err != nil {
		w.lastError = err.Error()
	}
}

func (w *TargetWorker) runOnce(ctx context.Context) error {
	records, err := w.Store.ListLogicalAccountTargets(
		ctx,
		targetapp.StatusPending,
		targetapp.StatusConverging,
		targetapp.StatusBlocked,
		targetapp.StatusConverged,
	)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	var runErrors []error
	var failures []TargetFailure
	defer func() {
		w.mu.Lock()
		w.targetErrors = failures
		w.mu.Unlock()
	}()
	timeout := w.ConvergeTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		candidateCtx, cancel := context.WithTimeout(ctx, timeout)
		result, convergeErr := w.Executor.Converge(
			candidateCtx,
			record.SpaceID,
			record.LogicalAccountID,
		)
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		if convergeErr != nil {
			if accountErr, ok := convergeErr.(*targetapp.AccountError); ok {
				failures = append(failures, TargetFailure{
					SpaceID: record.SpaceID, LogicalAccountID: record.LogicalAccountID,
					TargetID: record.TargetID, TradingAccountID: accountErr.TradingAccountID,
					Error: convergeErr.Error(),
				})
			} else {
				runErrors = append(runErrors, convergeErr)
			}
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
	outcome := "success"
	switch {
	case err != nil:
		outcome = "error"
	case result.Status == targetapp.StatusConverged:
		outcome = "success"
	case result.Status == targetapp.StatusBlocked ||
		result.Status == targetapp.StatusPaused:
		outcome = "rejected"
	}
	_ = w.Metrics.ObserveRun("target_commit", outcome, "trade-rebalance", at)
}

func (w *TargetWorker) initWake() {
	w.wakeOnce.Do(func() {
		w.wake = make(chan struct{}, 1)
	})
}
