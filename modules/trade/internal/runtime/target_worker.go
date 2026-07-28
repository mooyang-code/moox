package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

var ErrTargetWorkerConfig = errors.New("trade runtime: TargetWorker is not configured")

type TargetExecutionStore interface {
	ListTargetExecutions(context.Context, ...string) ([]store.TargetExecutionRecord, error)
	UpdateTargetExecutionState(context.Context, store.TargetExecutionRecord) (bool, error)
}

type TargetConverger interface {
	Converge(context.Context, string, string) (targetapp.Result, error)
}

type TargetWorker struct {
	Store    TargetExecutionStore
	Executor TargetConverger
	Interval time.Duration
	Now      func() time.Time

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
	records, err := w.Store.ListTargetExecutions(
		ctx,
		targetapp.StatusRunning,
		targetapp.StatusPaused,
	)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now()
	}
	conflicts := conflictingTargetExecutions(records, now)
	var runErrors []error
	for _, record := range records {
		if record.NotAfter <= now.UnixMilli() {
			if _, convergeErr := w.Executor.Converge(
				ctx,
				record.SpaceID,
				record.ExecutionBindingID,
			); convergeErr != nil {
				runErrors = append(runErrors, convergeErr)
			}
			continue
		}
		if conflict := conflicts[targetExecutionKey(record)]; conflict != "" {
			record.Status = targetapp.StatusPaused
			record.LastError = conflict
			if _, updateErr := w.Store.UpdateTargetExecutionState(ctx, record); updateErr != nil {
				runErrors = append(runErrors, updateErr)
			}
			continue
		}
		if _, convergeErr := w.Executor.Converge(
			ctx,
			record.SpaceID,
			record.ExecutionBindingID,
		); convergeErr != nil {
			runErrors = append(runErrors, convergeErr)
		}
	}
	return errors.Join(runErrors...)
}

func conflictingTargetExecutions(
	records []store.TargetExecutionRecord,
	now time.Time,
) map[string]string {
	lanes := make(map[string][]string)
	for _, record := range records {
		if record.NotAfter <= now.UnixMilli() {
			continue
		}
		for _, target := range record.Targets {
			lane := record.SpaceID + "\x00" + record.ExchangeAccountID + "\x00" + target.Symbol
			lanes[lane] = append(lanes[lane], targetExecutionKey(record))
		}
	}
	conflicts := make(map[string]string)
	for lane, keys := range lanes {
		if len(keys) < 2 {
			continue
		}
		cause := fmt.Sprintf(
			"trade target: multiple active execution bindings for lane %q",
			lane,
		)
		for _, key := range keys {
			conflicts[key] = cause
		}
	}
	return conflicts
}

func targetExecutionKey(record store.TargetExecutionRecord) string {
	return record.SpaceID + "\x00" + record.ExecutionBindingID
}

func (w *TargetWorker) initWake() {
	w.wakeOnce.Do(func() {
		w.wake = make(chan struct{}, 1)
	})
}
