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
	Actions       RunningActionSource
	Resumer       OperatorActionResumer
	Interval      time.Duration
	ActionTimeout time.Duration
	// ActionScoped 判断一个动作的恢复错误是否只关乎这个动作（账户、交易所侧的问题，例如凭证失效、标的已下架）：
	// 是的只记在 ActionErrors 里，不影响就绪；其余（数据库等共享故障）让工作线程不就绪。为空时所有错误都算共享故障。
	ActionScoped func(error) bool

	mu           sync.RWMutex
	ready        bool
	lastError    string
	actionErrors []OperatorActionFailure
}

// OperatorActionFailure 是一个运行中动作这一轮恢复失败的原因。
type OperatorActionFailure struct {
	SpaceID    string `json:"space_id"`
	ActionID   string `json:"action_id"`
	ActionType string `json:"action_type"`
	Error      string `json:"error"`
}

// OperatorWorkerSnapshot 是工作线程的状态。ActionErrors 按动作列出这一轮恢复失败的原因；只关乎单个动作的错误
// （见 OperatorWorker.ActionScoped）不影响 Ready：一个动作反复失败（例如账户凭证失效）不能拖垮整个 Trade 的就绪，
// 否则生命周期检查失败会自动暂停 Trade，所有组合都停止执行。
type OperatorWorkerSnapshot struct {
	Ready        bool
	LastError    string
	ActionErrors []OperatorActionFailure
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
		ActionErrors: append([]OperatorActionFailure(nil), w.actionErrors...),
	}
}

// setResult 记下一轮的结果：err 是共享故障（列不出运行中的动作、不只关乎单个动作的恢复错误），决定就绪；
// failures 是各动作的恢复失败。
func (w *OperatorWorker) setResult(failures []OperatorActionFailure, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ready = err == nil
	w.lastError = ""
	if err != nil {
		w.lastError = err.Error()
	}
	w.actionErrors = failures
}

// runOnce 恢复所有运行中的动作，各动作之间互不影响：一个动作失败照样恢复后面的，失败按动作记下；
// 返回的错误只含共享故障。
func (w *OperatorWorker) runOnce(ctx context.Context) ([]OperatorActionFailure, error) {
	if w == nil || w.Actions == nil || w.Resumer == nil {
		return nil, ErrOperatorWorkerConfig
	}
	actions, err := w.Actions.ListAllRunningOperatorActions(ctx)
	if err != nil {
		return nil, err
	}
	var failures []OperatorActionFailure
	var sharedErrors []error
	timeout := w.ActionTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	for _, action := range actions {
		if ctx.Err() != nil {
			break
		}
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		err := w.Resumer.ResumeOperatorAction(attemptCtx, action)
		cancel()
		if err != nil {
			failures = append(failures, OperatorActionFailure{
				SpaceID: action.SpaceID, ActionID: action.ActionID, ActionType: action.ActionType, Error: err.Error(),
			})
			if w.ActionScoped == nil || !w.ActionScoped(err) {
				sharedErrors = append(sharedErrors, err)
			}
		}
	}
	return failures, errors.Join(sharedErrors...)
}
