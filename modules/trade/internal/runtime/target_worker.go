package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

var ErrTargetWorkerConfig = errors.New("trade runtime: TargetWorker is not configured")

type LogicalTargetStore interface {
	GetLogicalAccountTarget(context.Context, string, string) (store.LogicalAccountTargetRecord, error)
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
	// Gate 非空时是生命周期门禁：返回 false（系统或手动暂停、恢复追赶、重置）时不再收敛任何目标，
	// 已经提交的订单请求照常结束，由 Quiesce 等待。暂停不撤单、不平仓，未收敛的目标保持原状。
	Gate func() bool
	// inflight 是正在收敛（可能正在向交易所提交订单）的目标数。
	inflight atomic.Int64

	wakeOnce   sync.Once
	wake       chan struct{}
	targetWake chan struct{}
	queueMu    sync.Mutex
	pending    map[targetKey]struct{}
	queue      []targetKey

	mu           sync.RWMutex
	ready        bool
	lastError    string
	targetErrors []TargetFailure
	expiredMu    sync.Mutex
	expiredSweep bool
	expiredRetry map[targetKey]struct{}
}

type targetKey struct {
	spaceID          string
	logicalAccountID string
}

func (w *TargetWorker) WakeTarget(spaceID, logicalAccountID string) {
	if w == nil || spaceID == "" || logicalAccountID == "" {
		return
	}
	w.initWake()
	key := targetKey{spaceID, logicalAccountID}
	w.queueMu.Lock()
	if _, exists := w.pending[key]; !exists {
		w.pending[key] = struct{}{}
		w.queue = append(w.queue, key)
	}
	w.queueMu.Unlock()
	select {
	case w.targetWake <- struct{}{}:
	default:
	}
}

func (w *TargetWorker) takeTargets() []targetKey {
	w.queueMu.Lock()
	defer w.queueMu.Unlock()
	// Detach before I/O so a wake arriving during convergence queues a new pass.
	keys := w.queue
	w.queue = nil
	w.pending = make(map[targetKey]struct{})
	return keys
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
		case <-w.targetWake:
			// Only a full scan can establish that an earlier shared failure recovered.
			if err := w.runTargets(ctx, w.takeTargets()); err != nil {
				w.setResult(err)
			}
			continue
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

// open 报告生命周期门禁是否放行。
func (w *TargetWorker) open() bool { return w.Gate == nil || w.Gate() }

// Quiesce 等待在途的收敛（订单提交）结束；门禁关闭之后调用，才能确认没有订单还在路上。
func (w *TargetWorker) Quiesce(ctx context.Context) error {
	if w == nil {
		return nil
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for w.inflight.Load() > 0 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("仍有 %d 个目标在收敛：%w", w.inflight.Load(), ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

func (w *TargetWorker) runOnce(ctx context.Context) error {
	if !w.open() {
		return nil
	}
	statuses := []string{targetapp.StatusPending, targetapp.StatusConverging, targetapp.StatusBlocked, targetapp.StatusConverged}
	w.expiredMu.Lock()
	if w.expiredRetry == nil {
		w.expiredRetry = make(map[targetKey]struct{})
	}
	includeExpired := !w.expiredSweep
	w.expiredMu.Unlock()
	if includeExpired {
		statuses = append(statuses, targetapp.StatusExpired)
	}
	records, err := w.Store.ListLogicalAccountTargets(ctx, statuses...)
	if err != nil {
		return err
	}
	w.expiredMu.Lock()
	if includeExpired {
		w.expiredSweep = true
	}
	for _, record := range records {
		if record.Status == targetapp.StatusExpired {
			w.expiredRetry[targetKey{record.SpaceID, record.LogicalAccountID}] = struct{}{}
		}
	}
	retry := make([]targetKey, 0, len(w.expiredRetry))
	for key := range w.expiredRetry {
		retry = append(retry, key)
	}
	w.expiredMu.Unlock()
	recordKeys := make(map[targetKey]struct{}, len(records))
	for _, record := range records {
		recordKeys[targetKey{record.SpaceID, record.LogicalAccountID}] = struct{}{}
	}
	for _, key := range retry {
		if _, found := recordKeys[key]; found {
			continue
		}
		record, getErr := w.Store.GetLogicalAccountTarget(ctx, key.spaceID, key.logicalAccountID)
		if errors.Is(getErr, gorm.ErrRecordNotFound) {
			w.expiredMu.Lock()
			delete(w.expiredRetry, key)
			w.expiredMu.Unlock()
			continue
		}
		if getErr != nil {
			return getErr
		}
		if record.Status == targetapp.StatusExpired {
			records = append(records, record)
			recordKeys[key] = struct{}{}
		} else {
			w.expiredMu.Lock()
			delete(w.expiredRetry, key)
			w.expiredMu.Unlock()
		}
	}
	return w.runRecords(ctx, records, true)
}

func (w *TargetWorker) runTargets(ctx context.Context, keys []targetKey) error {
	if !w.open() {
		return nil
	}
	var runErrors []error
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := w.Store.GetLogicalAccountTarget(ctx, key.spaceID, key.logicalAccountID)
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, store.ErrTargetExpired) {
			continue
		}
		if err != nil {
			runErrors = append(runErrors, err)
			continue
		}
		switch record.Status {
		case targetapp.StatusPending, targetapp.StatusConverging, targetapp.StatusBlocked, targetapp.StatusConverged, targetapp.StatusExpired:
			runErrors = append(runErrors, w.runRecords(ctx, []store.LogicalAccountTargetRecord{record}, false))
		}
	}
	return errors.Join(runErrors...)
}

func (w *TargetWorker) runRecords(ctx context.Context, records []store.LogicalAccountTargetRecord, full bool) error {
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	var runErrors []error
	var failures []TargetFailure
	defer func() {
		w.mu.Lock()
		if !full {
			for _, previous := range w.targetErrors {
				matched := false
				for _, record := range records {
					if previous.SpaceID == record.SpaceID && previous.LogicalAccountID == record.LogicalAccountID {
						matched = true
						break
					}
				}
				if !matched {
					failures = append(failures, previous)
				}
			}
		}
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
		// 批量中途被暂停：后面的目标不再收敛（已经开始的那一个照常结束）。
		if !w.open() {
			return errors.Join(runErrors...)
		}
		candidateCtx, cancel := context.WithTimeout(ctx, timeout)
		w.inflight.Add(1)
		result, convergeErr := w.Executor.Converge(
			candidateCtx,
			record.SpaceID,
			record.LogicalAccountID,
		)
		w.inflight.Add(-1)
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		if convergeErr != nil {
			if !full && convergeErr == gorm.ErrRecordNotFound {
				continue
			}
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
		if record.Status == targetapp.StatusExpired {
			w.expiredMu.Lock()
			if w.expiredRetry == nil {
				w.expiredRetry = make(map[targetKey]struct{})
			}
			if convergeErr == nil {
				delete(w.expiredRetry, targetKey{record.SpaceID, record.LogicalAccountID})
			} else {
				w.expiredRetry[targetKey{record.SpaceID, record.LogicalAccountID}] = struct{}{}
			}
			w.expiredMu.Unlock()
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
		w.targetWake = make(chan struct{}, 1)
		w.pending = make(map[targetKey]struct{})
		w.expiredRetry = make(map[targetKey]struct{})
	})
}
