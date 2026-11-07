package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/catalog"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/delivery"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// Sender 是通知发送器（delivery.Sender 实现它，测试用假的）。
type Sender interface {
	Send(ctx context.Context, target delivery.Target, request delivery.Request) delivery.Result
}

// Worker 处理执行记录：和 JetStream 的消费解耦，一条通知失败不会拖慢同批后面的事件。
type Worker struct {
	Store   *store.Store
	Sender  Sender
	Actions *action.Registry
	// Batch 是每轮最多同时处理的执行记录数。
	Batch        int
	PollInterval time.Duration
	// RetryWindow 是从第一次失败起允许重试的最长时间。
	RetryWindow time.Duration
	// RetentionDays 是执行记录与回执的保留天数。
	RetentionDays int
	Now           func() time.Time

	once     sync.Once
	slots    chan struct{}
	mu       sync.Mutex
	running  map[string]int
	inflight sync.WaitGroup
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// StaleRunningAfter 是记录停在“执行中”超过多久还没有结果就放回待处理。
const StaleRunningAfter = 10 * time.Minute

// stepTimeout 是一次处理（发送通知或推进系统动作一步）的时限：只有一半的 StaleRunningAfter，
// 这样记录被当作卡住放回去之前，旧线程已经被取消，不会和重新领取它的线程同时做同一步（比如重复平仓）。
const stepTimeout = StaleRunningAfter / 2

// MaxPerRule 是同一条规则同时处理的执行记录数上限：一条规则的接口很慢时，不会占住全部处理能力。
const MaxPerRule = 2

// Run 一直处理到 ctx 结束：启动时把上次停在“执行中”的记录放回待处理，之后按间隔领取到期的记录（领取后立即返回，
// 处理在各自的 goroutine 里并发进行，慢接口不会拖住下一轮），每分钟把卡在“执行中”太久的记录放回待处理，每小时清理一次过期记录。
func (w *Worker) Run(ctx context.Context) error {
	if recovered, err := w.Store.RecoverRunning(ctx, w.now().UnixMilli()); err != nil {
		return fmt.Errorf("恢复执行中的记录失败: %w", err)
	} else if recovered > 0 {
		log.InfoContextf(ctx, "已把 %d 条上次中断的执行记录放回待处理", recovered)
	}
	poll := time.NewTicker(w.PollInterval)
	defer poll.Stop()
	stale := time.NewTicker(time.Minute)
	defer stale.Stop()
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()
	defer w.inflight.Wait()
	for {
		w.dispatch(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stale.C:
			w.recoverStale(ctx)
		case <-purge.C:
			w.purge(ctx)
		case <-poll.C:
		}
	}
}

func (w *Worker) recoverStale(ctx context.Context) {
	now := w.now()
	recovered, err := w.Store.RecoverStaleRunning(ctx, now.Add(-StaleRunningAfter).UnixMilli(), now.UnixMilli())
	if err != nil {
		log.WarnContextf(ctx, "放回卡住的执行记录失败: %v", err)
		return
	}
	if recovered > 0 {
		log.WarnContextf(ctx, "已把 %d 条停在执行中超过 %s 的记录放回待处理", recovered, StaleRunningAfter)
	}
}

func (w *Worker) purge(ctx context.Context) {
	cutoff := w.now().Add(-time.Duration(w.RetentionDays) * 24 * time.Hour).UnixMilli()
	if err := w.Store.PurgeBefore(ctx, cutoff); err != nil {
		log.WarnContextf(ctx, "清理过期执行记录失败: %v", err)
	}
}

// RunOnce 领取到期的记录并等它们处理完，返回处理的条数（测试与一次性调用用；Run 不等待）。
func (w *Worker) RunOnce(ctx context.Context) int {
	launched := w.dispatch(ctx)
	w.inflight.Wait()
	return launched
}

// dispatch 按空闲的处理名额领取到期的记录，各自在 goroutine 里处理，领取完立即返回；返回启动的个数。
// 每条规则同时处理的数量不超过 MaxPerRule：领取时在库里按规则的剩余名额限制，占满的规则这一轮一条也不领。
func (w *Worker) dispatch(ctx context.Context) int {
	w.once.Do(func() {
		w.slots = make(chan struct{}, w.Batch)
		w.running = map[string]int{}
	})
	free := cap(w.slots) - len(w.slots)
	if free <= 0 {
		return 0
	}
	w.mu.Lock()
	remaining := map[string]int{}
	for rule, count := range w.running {
		if count > 0 {
			remaining[rule] = max(MaxPerRule-count, 0)
		}
	}
	w.mu.Unlock()
	claimed, err := w.Store.ClaimDueLimited(ctx, w.now().UnixMilli(), free, w.Actions.Actions(), MaxPerRule, remaining)
	if err != nil {
		if ctx.Err() == nil {
			log.WarnContextf(ctx, "领取到期的执行记录失败: %v", err)
		}
		return 0
	}
	for _, execution := range claimed {
		w.mu.Lock()
		w.running[execution.RuleID]++
		w.mu.Unlock()
		w.slots <- struct{}{}
		w.inflight.Add(1)
		go func() {
			defer func() {
				w.mu.Lock()
				w.running[execution.RuleID]--
				w.mu.Unlock()
				<-w.slots
				w.inflight.Done()
			}()
			w.process(ctx, execution)
		}()
	}
	return len(claimed)
}

func (w *Worker) process(ctx context.Context, execution store.Execution) {
	// 处理中途 panic 不能让记录一直停在执行中：当作一次失败的尝试。
	defer func() {
		if recovered := recover(); recovered != nil {
			log.ErrorContextf(ctx, "处理执行记录 %s 时出现 panic: %v", execution.ExecutionID, recovered)
			w.reschedule(ctx, execution, "内部错误，稍后重试")
		}
	}()
	rule, err := w.Store.GetRule(ctx, execution.RuleID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			w.save(ctx, execution, store.Result{Status: store.StatusCanceled, LastError: "规则已删除", Attempts: execution.Attempts, Failures: execution.Failures, FinishedAt: w.now().UnixMilli()})
			return
		}
		w.reschedule(ctx, execution, "读取规则失败: "+err.Error())
		return
	}
	if execution.ActionType == store.ActionSystem {
		w.processSystem(ctx, execution, rule)
		return
	}
	w.processNotify(ctx, execution, rule)
}

func (w *Worker) processNotify(ctx context.Context, execution store.Execution, rule store.Rule) {
	entry, _ := catalog.Lookup(execution.EventName)
	callCtx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	result := w.Sender.Send(callCtx, delivery.Target{URL: rule.NotifyURL, Secret: rule.Secret, AllowPrivate: rule.AllowPrivate}, requestFor(execution, entry))
	now := w.now()
	attempt := execution.Attempts + 1
	record := store.Attempt{Attempt: attempt, At: now.UnixMilli(), HTTPStatus: result.StatusCode, DurationMS: result.Duration.Milliseconds(), ResponseSnippet: result.Snippet}
	if result.Err != nil {
		record.Error = result.Err.Error()
	}
	update := store.Result{Attempts: attempt, Failures: execution.Failures, FirstFailedAt: execution.FirstFailedAt, DurationMS: result.Duration.Milliseconds(), Attempt: &record}
	switch result.Class {
	case delivery.Success:
		update.Status, update.FinishedAt = store.StatusSucceeded, now.UnixMilli()
	case delivery.Permanent:
		update.Status, update.LastError, update.FinishedAt = store.StatusFailed, record.Error, now.UnixMilli()
	default:
		update.Failures++
		if update.FirstFailedAt == 0 {
			update.FirstFailedAt = now.UnixMilli()
		}
		update.LastError = record.Error
		delay, ok := delivery.NextDelay(update.Failures)
		if ok && now.Add(delay).Sub(time.UnixMilli(update.FirstFailedAt)) <= w.RetryWindow {
			update.Status, update.NextRunAt = store.StatusPending, now.Add(delay).UnixMilli()
		} else {
			update.Status, update.FinishedAt = store.StatusFailed, now.UnixMilli()
		}
	}
	w.save(ctx, execution, update)
}

func (w *Worker) processSystem(ctx context.Context, execution store.Execution, rule store.Rule) {
	handler, ok := w.Actions.Handler(rule.SystemAction)
	if !ok {
		// 领取时已按注册的处理器过滤，走到这里说明处理器被撤掉了：放回去等。
		w.reschedule(ctx, execution, "系统动作暂时没有处理器")
		return
	}
	now := w.now()
	stepCtx, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	outcome, err := handler.Step(stepCtx, execution, now)
	if err != nil {
		w.reschedule(ctx, execution, err.Error())
		return
	}
	update := store.Result{Attempts: execution.Attempts + 1, LastError: outcome.Error, ProcessedPayload: execution.PayloadJSON}
	switch outcome.Status {
	case store.StatusPending:
		next := outcome.NextRunAt
		if next.IsZero() {
			next = now.Add(time.Minute)
		}
		update.Status, update.NextRunAt = store.StatusPending, next.UnixMilli()
		// 待处理并带着问题说明，是这次推进遇到了暂时失败（Trade 不可达等）：累计连续次数，关注项据此提醒持续失败；
		// 一次没有问题的推进就清零。
		if outcome.Error != "" {
			update.Failures, update.FirstFailedAt = transientFailure(execution, now)
		}
	case store.StatusSucceeded, store.StatusFailed, store.StatusCanceled:
		update.Status, update.FinishedAt = outcome.Status, now.UnixMilli()
	default:
		w.reschedule(ctx, execution, fmt.Sprintf("系统动作返回了未知状态 %q", outcome.Status))
		return
	}
	w.save(ctx, execution, update)
}

// reschedule 把记录放回待处理，一分钟后再试。系统动作的记录算一次暂时失败（累计连续次数）；
// 通知的记录不计：它的失败次数决定重试间隔和重试期限，读不到规则这类本地问题不能把它提前判为失败。
func (w *Worker) reschedule(ctx context.Context, execution store.Execution, reason string) {
	now := w.now()
	failures, firstFailedAt := execution.Failures, execution.FirstFailedAt
	if execution.ActionType == store.ActionSystem {
		failures, firstFailedAt = transientFailure(execution, now)
	}
	w.save(ctx, execution, store.Result{
		Status: store.StatusPending, NextRunAt: now.Add(time.Minute).UnixMilli(), LastError: reason,
		Attempts: execution.Attempts, Failures: failures, FirstFailedAt: firstFailedAt, ProcessedPayload: execution.PayloadJSON,
	})
}

// transientFailure 返回系统动作的记录再记一次暂时失败后的连续失败次数和第一次失败的时间。
func transientFailure(execution store.Execution, now time.Time) (int, int64) {
	firstFailedAt := execution.FirstFailedAt
	if execution.Failures == 0 || firstFailedAt == 0 {
		firstFailedAt = now.UnixMilli()
	}
	return execution.Failures + 1, firstFailedAt
}

func (w *Worker) save(ctx context.Context, execution store.Execution, result store.Result) {
	if _, err := w.Store.SaveResult(ctx, execution.ExecutionID, execution.ClaimedAt, result, w.now().UnixMilli()); err != nil && ctx.Err() == nil {
		log.WarnContextf(ctx, "保存执行记录 %s 的结果失败: %v", execution.ExecutionID, err)
	}
}

func requestFor(execution store.Execution, entry catalog.Entry) delivery.Request {
	var version uint32
	if entry.Event.Name() != "" {
		version = entry.Event.Version()
	}
	return delivery.Request{
		RuleID: execution.RuleID, EventID: execution.EventID, EventName: execution.EventName, EventVersion: version,
		SpaceID: execution.SpaceID, SubjectID: execution.SubjectID, OccurredAt: time.UnixMilli(execution.OccurredAt),
		PayloadJSON: execution.PayloadJSON,
	}
}
