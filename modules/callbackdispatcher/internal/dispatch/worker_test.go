package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/delivery"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/telemetry"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type fakeSender struct {
	mu       sync.Mutex
	results  []delivery.Result
	requests []delivery.Request
	targets  []delivery.Target
	onSend   func()
}

func (f *fakeSender) Send(_ context.Context, target delivery.Target, request delivery.Request) delivery.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onSend != nil {
		f.onSend()
	}
	f.requests, f.targets = append(f.requests, request), append(f.targets, target)
	result := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	return result
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newWorker(t *testing.T, s *store.Store, sender Sender, registry *action.Registry, c *clock) *Worker {
	t.Helper()
	if registry == nil {
		registry = action.NewRegistry()
	}
	return &Worker{Store: s, Sender: sender, Actions: registry, Batch: 8, PollInterval: time.Second, RetryWindow: 24 * time.Hour, RetentionDays: 30, Now: c.Now}
}

func seedNotify(t *testing.T, s *store.Store, c *clock) store.Execution {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.CreateRule(ctx, notifyRule("r1")))
	intake := newIntake(t, s)
	intake.Now = c.Now
	require.NoError(t, intake.accept(ctx, mustEntry(t), messageFor(t), `{"exchange":"binance"}`))
	executions := listAll(t, s)
	require.Len(t, executions, 1)
	return executions[0]
}

func TestNotifySuccessRecordsAttempt(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedNotify(t, s, c)
	sender := &fakeSender{results: []delivery.Result{{Class: delivery.Success, StatusCode: 200, Duration: 12 * time.Millisecond, Snippet: "ok"}}}
	worker := newWorker(t, s, sender, nil, c)

	require.Equal(t, 1, worker.RunOnce(ctx))
	require.Equal(t, 0, worker.RunOnce(ctx), "已经处理完的不再领取")
	got, err := s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusSucceeded, got.Status)
	require.Equal(t, 1, got.Attempts)
	require.EqualValues(t, 12, got.DurationMS)
	require.Equal(t, baseTime.UnixMilli(), got.FinishedAt)
	require.Zero(t, got.NextRunAt)
	attempts, err := s.ListAttempts(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Equal(t, 200, attempts[0].HTTPStatus)
	require.Equal(t, "ok", attempts[0].ResponseSnippet)

	// 请求里带的是规则的地址、密钥和事件内容。
	require.Equal(t, "https://example.com/hook", sender.targets[0].URL)
	require.Equal(t, "secret", sender.targets[0].Secret)
	require.Equal(t, seeded.EventID, sender.requests[0].EventID)
	require.EqualValues(t, 1, sender.requests[0].EventVersion)
	require.Equal(t, `{"exchange":"binance"}`, sender.requests[0].PayloadJSON)
}

func TestNotifyRetriesOnScheduleThenFails(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedNotify(t, s, c)
	sender := &fakeSender{results: []delivery.Result{{Class: delivery.Retryable, StatusCode: 500, Err: errors.New("对方返回状态码 500")}}}
	worker := newWorker(t, s, sender, nil, c)

	wantDelays := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour}
	for i, delay := range wantDelays {
		require.Equal(t, 1, worker.RunOnce(ctx), "第 %d 次尝试", i+1)
		got, err := s.GetExecution(ctx, seeded.ExecutionID)
		require.NoError(t, err)
		require.Equal(t, store.StatusPending, got.Status)
		require.Equal(t, i+1, got.Failures)
		require.Equal(t, c.now.Add(delay).UnixMilli(), got.NextRunAt)
		require.Equal(t, baseTime.UnixMilli(), got.FirstFailedAt)
		require.Equal(t, "对方返回状态码 500", got.LastError)
		// 没到时间不领取。
		c.now = c.now.Add(delay - time.Second)
		require.Equal(t, 0, worker.RunOnce(ctx))
		c.now = c.now.Add(time.Second)
	}
	require.Equal(t, 1, worker.RunOnce(ctx), "第 7 次尝试")
	got, err := s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusFailed, got.Status, "重试时间表用完")
	require.Equal(t, 7, got.Attempts)
	require.Zero(t, got.NextRunAt)
	attempts, err := s.ListAttempts(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Len(t, attempts, 7)

	// 重新执行：失败计数清零，重试间隔重新从 1 分钟开始，尝试编号继续往后。
	_, err = s.RetryExecution(ctx, seeded.ExecutionID, c.now.UnixMilli())
	require.NoError(t, err)
	require.Equal(t, 1, worker.RunOnce(ctx))
	got, err = s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusPending, got.Status)
	require.Equal(t, 1, got.Failures)
	require.Equal(t, 8, got.Attempts)
	require.Equal(t, c.now.Add(time.Minute).UnixMilli(), got.NextRunAt)
}

func TestNotifyStopsWhenRetryWindowWouldBeExceeded(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedNotify(t, s, c)
	sender := &fakeSender{results: []delivery.Result{{Class: delivery.Retryable, Err: errors.New("连接失败")}}}
	worker := newWorker(t, s, sender, nil, c)
	worker.RetryWindow = 10 * time.Minute // 第 3 次失败后的 30 分钟间隔会越过窗口
	for range 3 {
		require.Equal(t, 1, worker.RunOnce(ctx))
		got, err := s.GetExecution(ctx, seeded.ExecutionID)
		require.NoError(t, err)
		if got.Status == store.StatusPending {
			c.now = time.UnixMilli(got.NextRunAt)
		}
	}
	got, err := s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusFailed, got.Status)
	require.Equal(t, 3, got.Attempts)
}

func TestNotifyPermanentFailureDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedNotify(t, s, c)
	sender := &fakeSender{results: []delivery.Result{{Class: delivery.Permanent, StatusCode: 404, Snippet: "not found", Err: errors.New("对方返回状态码 404")}}}
	worker := newWorker(t, s, sender, nil, c)
	require.Equal(t, 1, worker.RunOnce(ctx))
	got, err := s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusFailed, got.Status)
	require.Equal(t, 1, got.Attempts)
	require.Equal(t, "对方返回状态码 404", got.LastError)

	// 失败且没确认的记录出现在关注项里，确认后消失。
	require.NoError(t, (&AttentionWorker{Store: s, Now: c.Now}).Publish(ctx))
	require.Equal(t, 1.0, testutil.ToFloat64(telemetry.Attention.WithLabelValues(telemetry.AttentionExecutionFailed, "规则 r1：对方返回状态码 404")))
	require.NoError(t, s.AcknowledgeExecution(ctx, seeded.ExecutionID, c.now.UnixMilli()))
	require.NoError(t, (&AttentionWorker{Store: s, Now: c.Now}).Publish(ctx))
	require.Equal(t, 0.0, testutil.ToFloat64(telemetry.Attention.WithLabelValues(telemetry.AttentionExecutionFailed, "")))
}

func TestCancelDuringDeliveryKeepsCanceled(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedNotify(t, s, c)
	sender := &fakeSender{results: []delivery.Result{{Class: delivery.Success, StatusCode: 200}}}
	sender.onSend = func() {
		// 发送过程中用户取消。
		_, err := s.CancelExecution(ctx, seeded.ExecutionID, c.now.UnixMilli())
		require.NoError(t, err)
	}
	worker := newWorker(t, s, sender, nil, c)
	require.Equal(t, 1, worker.RunOnce(ctx))
	got, err := s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusCanceled, got.Status)
}

func TestInterruptedRunningRecordsAreRecoveredOnStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedNotify(t, s, c)
	claimed, err := s.ClaimDue(ctx, c.now.UnixMilli(), 10, nil)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	sender := &fakeSender{results: []delivery.Result{{Class: delivery.Success, StatusCode: 200}}}
	worker := newWorker(t, s, sender, nil, c)
	worker.PollInterval = 10 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	require.Eventually(t, func() bool {
		got, err := s.GetExecution(context.Background(), seeded.ExecutionID)
		return err == nil && got.Status == store.StatusSucceeded
	}, 3*time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}

type fakeHandler struct {
	mu        sync.Mutex
	steps     []store.Execution
	outcomes  []action.Outcome
	stepErr   error
	cancelled []string
}

func (f *fakeHandler) Step(_ context.Context, execution store.Execution, _ time.Time) (action.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, execution)
	if f.stepErr != nil {
		return action.Outcome{}, f.stepErr
	}
	outcome := f.outcomes[0]
	if len(f.outcomes) > 1 {
		f.outcomes = f.outcomes[1:]
	}
	return outcome, nil
}

func (f *fakeHandler) OnCancel(_ context.Context, execution store.Execution) error {
	f.cancelled = append(f.cancelled, execution.ExecutionID)
	return nil
}

func seedSystem(t *testing.T, s *store.Store, c *clock) store.Execution {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.CreateRule(ctx, systemRule()))
	intake := newIntake(t, s)
	intake.Now = c.Now
	require.NoError(t, intake.accept(ctx, mustEntry(t), messageFor(t), `{"delist_at":"2026-11-11T08:00:00Z","scope":"ALL"}`))
	executions := listAll(t, s)
	require.Len(t, executions, 1)
	return executions[0]
}

func TestSystemExecutionWaitsUntilHandlerIsRegistered(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedSystem(t, s, c)
	sender := &fakeSender{results: []delivery.Result{{Class: delivery.Success}}}

	// 没有处理器：不领取，不空转，记录保持待处理。
	idle := newWorker(t, s, sender, nil, c)
	for range 3 {
		require.Equal(t, 0, idle.RunOnce(ctx))
	}
	got, err := s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusPending, got.Status)
	require.Zero(t, got.Attempts)

	// 注册之后立即处理：处理器可以多步推进，用 NextRunAt 约定下一次时间。
	handler := &fakeHandler{outcomes: []action.Outcome{
		{Status: store.StatusPending, NextRunAt: baseTime.Add(24 * time.Hour)},
		{Status: store.StatusSucceeded},
	}}
	registry := action.NewRegistry()
	registry.Register(store.SystemActionSellDelistedHoldings, handler)
	worker := newWorker(t, s, sender, registry, c)
	require.Equal(t, 1, worker.RunOnce(ctx))
	got, err = s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusPending, got.Status)
	require.Equal(t, baseTime.Add(24*time.Hour).UnixMilli(), got.NextRunAt)

	require.Equal(t, 0, worker.RunOnce(ctx), "没到下一步的时间")
	c.now = baseTime.Add(24 * time.Hour)
	require.Equal(t, 1, worker.RunOnce(ctx))
	got, err = s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusSucceeded, got.Status)
	require.Len(t, handler.steps, 2)
	require.Empty(t, sender.requests, "系统动作不发通知")
}

func TestSystemStepErrorIsRescheduledWithoutCountingFailure(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedSystem(t, s, c)
	handler := &fakeHandler{stepErr: errors.New("Trade 不可达")}
	registry := action.NewRegistry()
	registry.Register(store.SystemActionSellDelistedHoldings, handler)
	worker := newWorker(t, s, &fakeSender{results: []delivery.Result{{}}}, registry, c)
	require.Equal(t, 1, worker.RunOnce(ctx))
	got, err := s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusPending, got.Status)
	require.Equal(t, "Trade 不可达", got.LastError)
	require.Equal(t, baseTime.Add(time.Minute).UnixMilli(), got.NextRunAt)
	require.Zero(t, got.Failures)
}

func TestSystemFailureRaisesActionFailedAttention(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedSystem(t, s, c)
	handler := &fakeHandler{outcomes: []action.Outcome{{Status: store.StatusFailed, Error: "平仓用完重试"}}}
	registry := action.NewRegistry()
	registry.Register(store.SystemActionSellDelistedHoldings, handler)
	worker := newWorker(t, s, &fakeSender{results: []delivery.Result{{}}}, registry, c)
	require.Equal(t, 1, worker.RunOnce(ctx))
	got, err := s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusFailed, got.Status)

	attention := &AttentionWorker{Store: s, Now: c.Now}
	telemetry.NoteDelistDetected("binance:SPOT:FOOUSDT 有策略持有", c.now)
	require.NoError(t, attention.Publish(ctx))
	require.Equal(t, 1.0, testutil.ToFloat64(telemetry.Attention.WithLabelValues(telemetry.AttentionActionFailed, "系统规则：平仓用完重试")))
	require.Equal(t, 1.0, testutil.ToFloat64(telemetry.Attention.WithLabelValues(telemetry.AttentionDelistDetected, "binance:SPOT:FOOUSDT 有策略持有")))
	// delist_detected 只持续约 3 分钟。
	c.now = c.now.Add(telemetry.DelistDetectedWindow + time.Second)
	require.NoError(t, attention.Publish(ctx))
	require.Equal(t, 0.0, testutil.ToFloat64(telemetry.Attention.WithLabelValues(telemetry.AttentionDelistDetected, "")))
}

// 已取消但善后（解除禁止买入）没做成的系统动作记录，算作 action_failed，做成之后恢复。
func TestUnfinishedCancelCleanupRaisesActionFailedAttention(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedSystem(t, s, c)
	_, err := s.CancelExecution(ctx, seeded.ExecutionID, c.now.UnixMilli())
	require.NoError(t, err)

	attention := &AttentionWorker{Store: s, Now: c.Now}
	require.NoError(t, attention.Publish(ctx))
	require.Equal(t, 1.0, testutil.ToFloat64(telemetry.Attention.WithLabelValues(telemetry.AttentionActionFailed, "系统规则：取消后的善后未完成，请在执行记录里再取消一次")))

	require.NoError(t, s.FinishCleanup(ctx, seeded.ExecutionID, c.now.UnixMilli()))
	require.NoError(t, attention.Publish(ctx))
	require.Equal(t, 0.0, testutil.ToFloat64(telemetry.Attention.WithLabelValues(telemetry.AttentionActionFailed, "")))
}

func TestDeletedRuleCancelsPendingExecution(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	c := &clock{now: baseTime}
	seeded := seedNotify(t, s, c)
	require.NoError(t, s.DeleteRule(ctx, "r1", c.now.UnixMilli()))
	worker := newWorker(t, s, &fakeSender{results: []delivery.Result{{Class: delivery.Success}}}, nil, c)
	require.Equal(t, 0, worker.RunOnce(ctx))
	got, err := s.GetExecution(ctx, seeded.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, store.StatusCanceled, got.Status)
}
