package runtime

import (
	"context"
	"errors"
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
	_ = w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_ = w.runOnce(ctx)
		}
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
