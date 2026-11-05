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
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Run 一直处理到 ctx 结束：启动时把上次停在“执行中”的记录放回待处理，之后按间隔轮询，每小时清理一次过期记录。
func (w *Worker) Run(ctx context.Context) error {
	if recovered, err := w.Store.RecoverRunning(ctx, w.now().UnixMilli()); err != nil {
		return fmt.Errorf("恢复执行中的记录失败: %w", err)
	} else if recovered > 0 {
		log.InfoContextf(ctx, "已把 %d 条上次中断的执行记录放回待处理", recovered)
	}
	poll := time.NewTicker(w.PollInterval)
	defer poll.Stop()
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()
	for {
		w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-purge.C:
			w.purge(ctx)
		case <-poll.C:
		}
	}
}

func (w *Worker) purge(ctx context.Context) {
	cutoff := w.now().Add(-time.Duration(w.RetentionDays) * 24 * time.Hour).UnixMilli()
	if err := w.Store.PurgeBefore(ctx, cutoff); err != nil {
		log.WarnContextf(ctx, "清理过期执行记录失败: %v", err)
	}
}

// RunOnce 领取并处理所有到期的记录，返回处理的条数。
func (w *Worker) RunOnce(ctx context.Context) int {
	claimed, err := w.Store.ClaimDue(ctx, w.now().UnixMilli(), w.Batch, w.Actions.Actions())
	if err != nil {
		if ctx.Err() == nil {
			log.WarnContextf(ctx, "领取到期的执行记录失败: %v", err)
		}
		return 0
	}
	var group sync.WaitGroup
	for _, execution := range claimed {
		group.Add(1)
		go func() {
			defer group.Done()
			w.process(ctx, execution)
		}()
	}
	group.Wait()
	return len(claimed)
}

func (w *Worker) process(ctx context.Context, execution store.Execution) {
	// 处理中途 panic 不能让记录一直停在执行中：当作一次失败的尝试。
	defer func() {
		if recovered := recover(); recovered != nil {
			log.ErrorContextf(ctx, "处理执行记录 %s 时出现 panic: %v", execution.ExecutionID, recovered)
			w.save(ctx, execution, store.Result{
				Status: store.StatusPending, NextRunAt: w.now().Add(time.Minute).UnixMilli(), LastError: "内部错误，稍后重试",
				Attempts: execution.Attempts, Failures: execution.Failures, FirstFailedAt: execution.FirstFailedAt,
			})
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
	result := w.Sender.Send(ctx, delivery.Target{URL: rule.NotifyURL, Secret: rule.Secret, AllowPrivate: rule.AllowPrivate}, requestFor(execution, entry))
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
	outcome, err := handler.Step(ctx, execution, now)
	if err != nil {
		w.reschedule(ctx, execution, err.Error())
		return
	}
	update := store.Result{Attempts: execution.Attempts + 1, Failures: execution.Failures, FirstFailedAt: execution.FirstFailedAt, LastError: outcome.Error}
	switch outcome.Status {
	case store.StatusPending:
		next := outcome.NextRunAt
		if next.IsZero() {
			next = now.Add(time.Minute)
		}
		update.Status, update.NextRunAt = store.StatusPending, next.UnixMilli()
	case store.StatusSucceeded, store.StatusFailed, store.StatusCanceled:
		update.Status, update.FinishedAt = outcome.Status, now.UnixMilli()
	default:
		w.reschedule(ctx, execution, fmt.Sprintf("系统动作返回了未知状态 %q", outcome.Status))
		return
	}
	w.save(ctx, execution, update)
}

// reschedule 把记录放回待处理，一分钟后再试，不计入失败次数。
func (w *Worker) reschedule(ctx context.Context, execution store.Execution, reason string) {
	w.save(ctx, execution, store.Result{
		Status: store.StatusPending, NextRunAt: w.now().Add(time.Minute).UnixMilli(), LastError: reason,
		Attempts: execution.Attempts, Failures: execution.Failures, FirstFailedAt: execution.FirstFailedAt,
	})
}

func (w *Worker) save(ctx context.Context, execution store.Execution, result store.Result) {
	if _, err := w.Store.SaveResult(ctx, execution.ExecutionID, result, w.now().UnixMilli()); err != nil && ctx.Err() == nil {
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
