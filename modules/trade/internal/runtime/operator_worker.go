package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var ErrOperatorWorkerConfig = errors.New(
	"trade runtime: OperatorWorker is not configured",
)

type RunningActionSource interface {
	ListAllRunningOperatorActions(
		context.Context,
	) ([]store.OperatorActionRecord, error)
}

type OperatorActionResumer interface {
	ResumeOperatorAction(context.Context, store.OperatorActionRecord) error
}

type OperatorWorker struct {
	Actions  RunningActionSource
	Resumer  OperatorActionResumer
	Interval time.Duration

	mu        sync.RWMutex
	ready     bool
	lastError string
}

type OperatorWorkerSnapshot struct {
	Ready     bool
	LastError string
}

func (w *OperatorWorker) Run(ctx context.Context) error {
	if w == nil || w.Actions == nil || w.Resumer == nil {
		return ErrOperatorWorkerConfig
	}
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
		case <-ticker.C:
			w.setResult(w.runOnce(ctx))
		}
	}
}

func (w *OperatorWorker) Snapshot() OperatorWorkerSnapshot {
	if w == nil {
		return OperatorWorkerSnapshot{}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return OperatorWorkerSnapshot{
		Ready: w.ready, LastError: w.lastError,
	}
}

func (w *OperatorWorker) setResult(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready = err == nil
	w.lastError = ""
	if err != nil {
		w.lastError = err.Error()
	}
}

func (w *OperatorWorker) runOnce(ctx context.Context) error {
	if w == nil || w.Actions == nil || w.Resumer == nil {
		return ErrOperatorWorkerConfig
	}
	actions, err := w.Actions.ListAllRunningOperatorActions(ctx)
	if err != nil {
		return err
	}
	var runErrors []error
	for _, action := range actions {
		if err := w.Resumer.ResumeOperatorAction(ctx, action); err != nil {
			runErrors = append(runErrors, err)
		}
	}
	return errors.Join(runErrors...)
}
