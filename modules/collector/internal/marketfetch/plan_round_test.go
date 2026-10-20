package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// roundMetrics 给对账器挂上指标，并让失败起始时间跟随测试时钟。
func roundMetrics(h *planHarness, r *PlanReconciler) *Metrics {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.now = func() time.Time { return h.now }
	r.Metrics = metrics
	return metrics
}

func failureLabel(m *Metrics, reason string) float64 {
	return testutil.ToFloat64(m.assignmentFailure.WithLabelValues("crypto", reason))
}

func requireNoFailure(t *testing.T, m *Metrics) {
	t.Helper()
	for _, reason := range assignmentFailureReasons {
		require.Zerof(t, failureLabel(m, reason), "unexpected failure reason %s", reason)
	}
}

func heldError() error {
	return fmt.Errorf("collector publish lease rejected: %w", scfinvoker.ErrCollectorPublishLeaseHeld)
}

func TestWaitJobWithinReturnsNotFinishedInsteadOfAnError(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.JobWait, r.PollInterval = 90*time.Second, time.Millisecond
	h.cloud.runningAll = true

	// 本轮剩余 1 秒：余量取剩余时间的四分之一，最多等 0.75 秒就交还给调用方，而不是报错。
	// 耗时上界只确认它远小于 JobWait（1 小时），不依赖精确的调度时间。
	short, cancelShort := context.WithTimeout(context.Background(), time.Second)
	defer cancelShort()
	h.cloud.runningAll = true
	started := time.Now()
	_, terminal, err := r.waitJobWithin(short, "job-1")
	require.NoError(t, err)
	require.False(t, terminal)
	require.Less(t, time.Since(started), 10*time.Second)

	// 作业已经结束时，短时限下也能拿到结果。
	h.cloud.runningAll = false
	h.cloud.jobs["job-2"] = []scfinvoker.BatchItemResult{{NodeID: "fn", Success: true}}
	fresh, cancelFresh := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFresh()
	_, terminal, err = r.waitJobWithin(fresh, "job-2")
	require.NoError(t, err)
	require.True(t, terminal)

	// 父 ctx 已被取消或已到期：返回其错误，调用方照旧把它当成失败。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	h.cloud.hangBatch = true // 真实的云端查询在 ctx 取消后返回其错误
	_, terminal, err = r.waitJobWithin(canceled, "job-1")
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, terminal)
	expired, cancelExpired := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelExpired()
	time.Sleep(5 * time.Millisecond)
	_, _, err = r.waitJobWithin(expired, "job-1")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWaitJobWithinBoundsASlowQueryAndKeepsRealErrors(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.JobWait, r.PollInterval = 80*time.Millisecond, time.Millisecond

	// 云端查询一直卡住：局部上限到期后返回未结束，而不是等到父 ctx 到期。
	h.cloud.hangBatch = true
	started := time.Now()
	_, terminal, err := r.waitJobWithin(context.Background(), "job-1")
	require.NoError(t, err)
	require.False(t, terminal)
	require.Less(t, time.Since(started), time.Second)

	// 查询本身出错（不是超时）：原样返回，照旧记为 cloudnode 失败。
	h.cloud.hangBatch = false
	h.cloud.batchErr = errors.New("get batch results: gateway unavailable")
	_, _, err = r.waitJobWithin(context.Background(), "job-1")
	require.ErrorContains(t, err, "gateway unavailable")

	// 作业已结束：正常返回结果。
	h.cloud.batchErr = nil
	h.cloud.jobs["job-9"] = []scfinvoker.BatchItemResult{{NodeID: "fn", Success: true}}
	items, terminal, err := r.waitJobWithin(context.Background(), "job-9")
	require.NoError(t, err)
	require.True(t, terminal)
	require.Len(t, items, 1)
}

func TestSlowCloudJobIsInFlightNotAFailure(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	r.JobWait = 90 * time.Second // 生产环境从未设置，默认 90 秒比单轮时限还长
	h.cloud.runningAll = true

	// 单轮时限 2 秒（生产是 55 秒）：旧实现一直等到 ctx 到期，再返回 context deadline exceeded 并记为 cloudnode 失败；
	// 现在等到剩余时间的四分之三就把作业交给下一轮，本轮正常结束。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, r.Reconcile(ctx))

	requireNoFailure(t, metrics)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
	require.Len(t, r.pending, 1, "作业记入在途集合，下一轮继续")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Equal(t, float64(r.pending[0].at.Unix()), testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")), "pending 起点是在途作业的提交时间")
	require.Zero(t, testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")))

	// 下一轮：作业仍在运行，被 settlePending 拦住，同样不是失败，也不会重复提交。
	submits := h.cloud.submits
	require.NoError(t, r.Reconcile(context.Background()))
	require.Equal(t, submits, h.cloud.submits)
	requireNoFailure(t, metrics)

	// 作业结束后继续推进。
	h.cloud.runningAll = false
	require.NoError(t, r.Reconcile(context.Background()))
	require.Empty(t, r.pending)
}

func TestPublishLeaseHeldIsClassifiedSeparatelyAndRecoversWhileThePlanIsStillOpen(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	since := func() float64 { return testutil.ToFloat64(metrics.assignmentFailureSince.WithLabelValues("crypto")) }

	h.cloud.acquireErr = heldError()
	err := r.Reconcile(context.Background())
	require.ErrorIs(t, err, scfinvoker.ErrCollectorPublishLeaseHeld)
	require.Equal(t, float64(1), failureLabel(metrics, ReasonPublishLeaseHeld))
	require.Zero(t, failureLabel(metrics, "cloudnode"))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
	first := since()
	require.Equal(t, float64(h.now.Unix()), first)

	// 持续被占用：同一原因，起始时间不重置。
	h.now = h.now.Add(3 * time.Minute)
	require.ErrorIs(t, r.Reconcile(context.Background()), scfinvoker.ErrCollectorPublishLeaseHeld)
	require.Equal(t, first, since())

	// 占用解除：计划仍然没有生效（还要等到窗口结束），但本轮没有失败，遗留的失败原因必须清掉，
	// 否则 Monitor 会在宽限期后继续报「租约长时间被占用」。
	h.cloud.acquireErr = nil
	h.now = h.now.Add(3 * time.Minute)
	require.NoError(t, r.Reconcile(context.Background()))
	require.NotNil(t, h.open(), "计划尚未生效")
	requireNoFailure(t, metrics)
	require.Zero(t, since())
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")), "计划进行中不刷新最近一次对账成功时间")
}

func TestOnlyAnExplicitConflictGetsTheLeaseHeldGrace(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		reason string
	}{
		"admin says held":     {heldError(), ReasonPublishLeaseHeld},
		"permission denied":   {errors.New("collector publish lease rejected: collector publish lease requires space owner or admin"), "cloudnode"},
		"admin unreachable":   {errors.New("dial tcp 127.0.0.1:11002: connect: connection refused"), "cloudnode"},
		"incomplete response": {errors.New("collector publish lease response identity is incomplete or inconsistent"), "cloudnode"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newPlanHarness(t, 60)
			r := h.reconciler()
			metrics := roundMetrics(h, r)
			h.cloud.acquireErr = tc.err
			require.Error(t, r.Reconcile(context.Background()))
			require.Equal(t, float64(1), failureLabel(metrics, tc.reason))
			for _, reason := range assignmentFailureReasons {
				if reason != tc.reason {
					require.Zero(t, failureLabel(metrics, reason))
				}
			}
		})
	}
}

func TestStaleReservationCleanupLeaseConflictIsClassifiedAsHeld(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	state, _ := BuildDesiredState(testPlanOptions(), testGroups(60, "1m"), nil)
	var name string
	for fn := range state.Functions {
		name = fn
		break
	}
	// 遗留预留需要受租约保护的删除作业：此时租约被占用，应归为「被占用」而不是 cloudnode。
	h.cloud.unready[name] = scfinvoker.Node{NodeID: name, FunctionName: name, TriggerType: "timer", ModifiedAt: h.now.Add(-time.Hour)}
	h.cloud.acquireErr = heldError()
	require.ErrorIs(t, r.Reconcile(context.Background()), scfinvoker.ErrCollectorPublishLeaseHeld)
	require.Equal(t, float64(1), failureLabel(metrics, ReasonPublishLeaseHeld))
	require.Zero(t, failureLabel(metrics, "cloudnode"))
}

// appliedFleet 把计划推进到生效，让云端读回与计划一致，并再对账一轮记录一次成功。
func appliedFleet(t *testing.T, h *planHarness, r *PlanReconciler) {
	t.Helper()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	for name, patch := range h.cloud.patches {
		h.cloud.nodes[name] = scfinvoker.Node{NodeID: name, Metadata: map[string]any{
			"assignment_hash": patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"], "timer_actual_enabled": true,
			"access_address": patch.GetManagedEnvironment()["MOOX_ACCESS_ADDRESS"], "access_id": patch.GetManagedEnvironment()["MOOX_ACCESS_ID"], "access_key_id": patch.GetManagedEnvironment()["MOOX_CALLER_KEY_ID"],
			"timer_available_status": "Available", "timer_actual_cron": patch.GetTimerCron(), "timer_actual_qualifier": timerTriggerQualifier, "timer_actual_message": timerTriggerMessage,
		}}
	}
	// 再对账一轮：没有未生效的计划、云端与计划一致，记录一次对账成功并完成首次读回核对。
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(r))
}

func tamperOneNode(h *planHarness) string {
	for name := range h.cloud.nodes {
		node := h.cloud.nodes[name]
		node.Metadata["assignment_hash"] = "tampered"
		h.cloud.nodes[name] = node
		return name
	}
	return ""
}

func TestDriftRepairStillRunningIsInFlightNotAFailure(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	appliedFleet(t, h, r)
	lastSuccess := testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto"))
	require.NotZero(t, lastSuccess)
	tamperOneNode(h)

	h.cloud.runningAll = true
	h.now = h.now.Add(2 * time.Hour)
	verifiedBefore := r.lastVerified
	require.NoError(t, h.reconcile(r))

	requireNoFailure(t, metrics)
	require.Equal(t, 1, r.aux.count(), "修复作业仍在云端运行，登记在途，结束前不会重复提交")
	require.Equal(t, verifiedBefore, r.lastVerified, "还没核对完毕，不更新读回时间")
	jobAt := r.aux.snapshot()[0].at
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Equal(t, float64(jobAt.Unix()), testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")))
	// 作业刚提交、没有超龄：对账本身正常，成功时间照常前进（大批量升级可能连续跑十几分钟，期间一直有在途作业）。
	fresh := testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto"))
	require.Equal(t, float64(h.now.Unix()), fresh)
	require.Greater(t, fresh, lastSuccess)

	// 在途期间再来一轮：同一批函数不会重复提交。
	submits := h.cloud.submits
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, submits, h.cloud.submits)

	// 最老的作业超过告警年龄仍在运行：说明卡住了，不再刷新成功时间，由 Monitor 的对账陈旧判定发现。
	h.now = jobAt.Add(pendingJobWarnAge + time.Minute)
	stuckBefore := testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto"))
	require.NoError(t, h.reconcile(r))
	requireNoFailure(t, metrics)
	require.Equal(t, stuckBefore, testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")))
	require.Equal(t, float64(jobAt.Unix()), testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")), "pending 起点仍是最早作业的提交时间")

	// 作业结束后恢复：成功时间前进，在途集合清空。
	h.cloud.runningAll = false
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Zero(t, r.aux.count())
	require.Greater(t, testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")), stuckBefore)
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
}

func TestUpgradePendingBeforeEnableStillFinishesTheRound(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	// 之前一轮因租约被占用而失败；现在升级作业已提交并在运行，EnableAll 返回 errCodeUpgradePending，
	// 管理器随后跳过本 tick 的对账：pending 要更新，遗留的失败原因要清掉。
	_ = r.fail(ReasonPublishLeaseHeld, heldError())
	r.aux.track(auxJob{id: "job-up", kind: auxJobKindUpgrade, functions: []string{"fn-a"}, at: r.now()})
	h.cloud.runningAll = true
	r.roundFailCount = r.failCount // 上一轮已结束
	r.finishRound(errCodeUpgradePending)

	requireNoFailure(t, metrics)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Equal(t, float64(r.now().Unix()), testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailureSince.WithLabelValues("crypto")))

	// 其他错误不是等待，不恢复健康。
	_ = r.fail("cloudnode", errors.New("boom"))
	r.roundFailCount = r.failCount
	r.finishRound(errors.New("enable timers: boom"))
	require.Equal(t, float64(1), failureLabel(metrics, "cloudnode"))
}

func TestDriftRepairThatFinishedWithFailuresIsStillAFailure(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	appliedFleet(t, h, r)
	name := tamperOneNode(h)
	h.cloud.failNodes[name] = true
	h.now = h.now.Add(2 * time.Hour)
	verifiedBefore := r.lastVerified

	require.Error(t, h.reconcile(r))
	require.Equal(t, float64(1), failureLabel(metrics, "cloudnode"))
	require.Equal(t, verifiedBefore, r.lastVerified, "有失败项：不更新读回时间，下一轮继续修复")
	require.Zero(t, r.aux.count(), "已到终态的失败作业不留在在途集合")
}

func TestCloudJobQueryErrorsStayCloudnodeFailures(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	h.cloud.batchErr = errors.New("get batch results: gateway unavailable")
	require.Error(t, r.Reconcile(context.Background()))
	require.Equal(t, float64(1), failureLabel(metrics, "cloudnode"))
	require.Zero(t, failureLabel(metrics, ReasonPublishLeaseHeld))
}

func TestFinishRoundDoesNotClearFailuresRecordedBetweenRounds(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	// 同一 tick 内对账之前的步骤（启用、升级）失败：对账本身没报错，也不能把它当成「本轮无失败」清掉。
	_ = r.fail("package", errors.New("resolve code package: unavailable"))
	require.NoError(t, r.Reconcile(context.Background()))
	require.Equal(t, float64(1), failureLabel(metrics, "cloudnode"))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))

	// 下一轮没有任何失败：恢复。
	require.NoError(t, r.Reconcile(context.Background()))
	requireNoFailure(t, metrics)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
}
