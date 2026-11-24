package fetchplan

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/cloudtest"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
)

// roundMetrics 给对账器挂上一份独立注册的指标。
func roundMetrics(_ *planHarness, r *PlanReconciler) *ControlMetrics {
	metrics := NewControlMetrics(prometheus.NewRegistry())
	r.Metrics = metrics
	return metrics
}

func failureLabel(m *ControlMetrics, reason string) float64 {
	return testutil.ToFloat64(m.assignmentFailure.WithLabelValues("crypto", reason))
}

func requireNoFailure(t *testing.T, m *ControlMetrics) {
	t.Helper()
	for _, reason := range assignmentFailureReasons {
		require.Zerof(t, failureLabel(m, reason), "unexpected failure reason %s", reason)
	}
}

// blockWrites 让写请求 request（function 为空表示任意函数）在执行前卡住，直到调用返回的 release；测试结束时自动放行。
func blockWrites(t *testing.T, h *planHarness, request, function string) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	h.cloud.client.BeforeWrite = func(name string, ref tencentscf.FunctionRef) error {
		if name == request && (function == "" || ref.FunctionName == function) {
			<-gate
		}
		return nil
	}
	return release
}

// failCreates 让建函数一律返回 err，返回解除的函数。
func failCreates(h *planHarness, err error) (recover func()) {
	var mu sync.Mutex
	active := true
	h.cloud.client.BeforeWrite = func(name string, _ tencentscf.FunctionRef) error {
		mu.Lock()
		defer mu.Unlock()
		if active && name == "CreateFunction" {
			return err
		}
		return nil
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		active = false
	}
}

func quotaError() error {
	return tcerrors.NewTencentCloudSDKError("LimitExceeded.Function", "函数个数超过上限", "req-quota")
}

// 一轮里只等到本轮的等待上限：超过上限还没做完的操作本轮返回“未结束”（不报错），下一轮再确认；
// 几毫秒就能做完的操作当轮就确认。
func TestWaitJobWithinReturnsNotFinishedInsteadOfAnError(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.opWaitMax = 50 * time.Millisecond
	release := blockWrites(t, h, "CreateFunction", "")
	started := time.Now()
	require.NoError(t, h.reconcile(r))
	require.Less(t, time.Since(started), 5*time.Second, "本轮最多等到等待上限就交还")
	progress, err := decodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.Empty(t, progress.Pushed, "建函数还没做完，本轮不确认")
	require.Empty(t, h.open().LastError)

	// 放行后下一轮确认，不重复起操作。
	release()
	h.waitOps(r)
	require.NoError(t, h.reconcile(r))
	progress, err = decodeFetchPlanProgress(h.lastRecord().Progress)
	require.NoError(t, err)
	require.Len(t, progress.Pushed, 2)
	require.Equal(t, 2, h.cloud.client.Count("CreateFunction"))

	// 有截止时间的一轮：等待上限是剩余时间减 5 秒；快的操作仍在当轮确认。
	fresh := newPlanHarness(t, 60)
	quick := fresh.reconciler()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, quick.Reconcile(ctx))
	progress, err = decodeFetchPlanProgress(fresh.lastRecord().Progress)
	require.NoError(t, err)
	require.Len(t, progress.Pushed, 2, "几毫秒就做完的操作当轮确认")
}

// 慢操作不会让一轮拖到超时；云端返回的真实错误（不是慢）照样按 cloud 报失败。
func TestWaitJobWithinBoundsASlowQueryAndKeepsRealErrors(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	r.opWaitMax = 50 * time.Millisecond
	release := blockWrites(t, h, "CreateFunction", "")
	started := time.Now()
	require.NoError(t, h.reconcile(r))
	require.Less(t, time.Since(started), 5*time.Second)
	requireNoFailure(t, metrics)
	release()
	h.waitOps(r)

	// 读云函数出错（不是超时、不是瞬时错误）：操作失败，按 cloud 上报，原因写进计划的 last_error。
	other := newPlanHarness(t, 60)
	failing := other.reconciler()
	otherMetrics := roundMetrics(other, failing)
	other.cloud.client.BeforeRead = func(name string, _ tencentscf.FunctionRef) error {
		if name == "GetFunction" {
			return errors.New("InternalError: gateway unavailable")
		}
		return nil
	}
	err := other.reconcile(failing)
	require.ErrorContains(t, err, "gateway unavailable")
	require.Equal(t, float64(1), failureLabel(otherMetrics, "cloud"))
	require.Contains(t, other.open().LastError, "gateway unavailable")
}

func TestSlowCloudJobIsInFlightNotAFailure(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	r.opWaitMax = 50 * time.Millisecond
	release := blockWrites(t, h, "CreateFunction", "")

	// 建函数要几分钟：本轮起了操作、等到上限就结束，不报失败；pending 的起点是操作的开始时间。
	startedAt := h.now
	require.NoError(t, r.Reconcile(context.Background()))
	requireNoFailure(t, metrics)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Equal(t, float64(startedAt.Unix()), testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")), "pending 起点是最早在跑的操作的开始时间")
	require.Zero(t, testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")))

	// 下一轮：操作仍在跑，本轮跳过这些函数，同样不是失败，也不会重复起操作。
	ensures := h.cloud.countOps("ensure", "")
	h.now = h.now.Add(20 * time.Second)
	require.NoError(t, r.Reconcile(context.Background()))
	require.Equal(t, ensures, h.cloud.countOps("ensure", ""))
	requireNoFailure(t, metrics)

	// 操作结束后下一轮确认，计划继续推进。
	release()
	h.waitOps(r)
	require.NoError(t, r.Reconcile(context.Background()))
	progress, err := decodeFetchPlanProgress(h.lastRecord().Progress)
	require.NoError(t, err)
	require.Len(t, progress.Pushed, 2)
	require.Equal(t, ensures, h.cloud.countOps("ensure", ""), "结束的操作只取结果，不重做")
}

// 配额超限单独归为 quota（Monitor 提示清理计划外的函数），持续超限时一直报 quota；
// 恢复后计划下发确认即生效，本轮没有失败，遗留的失败原因必须清掉。
func TestQuotaExceededIsClassifiedSeparatelyAndRecovers(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)

	recover := failCreates(h, quotaError())
	require.Error(t, r.Reconcile(context.Background()))
	require.Equal(t, float64(1), failureLabel(metrics, "quota"))
	require.Zero(t, failureLabel(metrics, "cloud"))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))

	// 持续超限：仍报 quota。
	h.now = h.now.Add(3 * time.Minute)
	require.Error(t, r.Reconcile(context.Background()))
	require.Equal(t, float64(1), failureLabel(metrics, "quota"))
	require.Zero(t, failureLabel(metrics, "cloud"))

	// 清掉计划外的函数之后：建函数成功，计划生效，按对账成功上报，失败原因清掉。
	recover()
	h.now = h.now.Add(20 * time.Second)
	require.NoError(t, r.Reconcile(context.Background()))
	require.Nil(t, h.open(), "下发确认就生效")
	requireNoFailure(t, metrics)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
	require.Equal(t, float64(h.now.Unix()), testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")), "本轮推进到生效：按对账成功上报")
}

// 只有函数个数、命名空间与账号预留并发的配额超限算 quota；参数越界、连不上、鉴权失败都按 cloud 报。
func TestOnlyQuotaErrorCodesAreClassifiedAsQuota(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		reason string
	}{
		"function quota":      {quotaError(), "quota"},
		"namespace quota":     {tcerrors.NewTencentCloudSDKError("LimitExceeded.Namespace", "命名空间个数超过上限", "req-1"), "quota"},
		"memory out of range": {tcerrors.NewTencentCloudSDKError("LimitExceeded.Memory", "内存越界", "req-2"), "cloud"},
		"auth failure":        {tcerrors.NewTencentCloudSDKError("AuthFailure.SecretIdNotFound", "SecretId not found", "req-3"), "cloud"},
		"unreachable":         {errors.New("ClientError.NetworkError: dial tcp: connect: connection refused"), "cloud"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newPlanHarness(t, 60)
			r := h.reconciler()
			metrics := roundMetrics(h, r)
			failCreates(h, tc.err)
			require.Error(t, r.Reconcile(context.Background()))
			require.Equal(t, float64(1), failureLabel(metrics, tc.reason))
			for _, reason := range assignmentFailureReasons {
				if reason != tc.reason {
					require.Zero(t, failureLabel(metrics, reason), reason)
				}
			}
		})
	}
}

// 建失败遗留的函数（CreateFailed）由 Ensure 先删再建；删除本身失败按 cloud 报，下一轮重来。
func TestFailedCreateCleanupErrorIsClassifiedAsCloud(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	name := planFunctionNames(t, 60)[0]
	h.scf.Seed(scfRef(name), &cloudtest.Function{Info: tencentscf.FunctionInfo{Status: "CreateFailed", Environment: map[string]string{}}})
	recover := h.failWrites("DeleteFunction", name)
	require.Error(t, r.Reconcile(context.Background()))
	require.Equal(t, float64(1), failureLabel(metrics, "cloud"))
	require.Zero(t, failureLabel(metrics, "quota"))
	require.Equal(t, "CreateFailed", h.mustFunction(name).Info.Status)

	recover()
	h.now = h.now.Add(20 * time.Second)
	require.NoError(t, r.Reconcile(context.Background()))
	require.Equal(t, "Active", h.mustFunction(name).Info.Status)
	requireNoFailure(t, metrics)
}

// appliedFleet 把计划推进到生效，再对账一轮：没有未生效的计划、云端与计划一致，记录一次对账成功并完成首次读回核对。
func appliedFleet(t *testing.T, h *planHarness, r *PlanReconciler) {
	t.Helper()
	h.applyFirstPlan(r)
	require.False(t, r.lastVerified.IsZero(), "首次读回核对完成")
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(r))
}

// tamperOne 改掉云上一个函数的分配哈希（模拟手工改动），返回函数名。
func tamperOne(h *planHarness) string {
	name := h.cloudTimers()[0]
	h.mutate(name, func(function *cloudtest.Function) {
		function.Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] = "tampered"
	})
	return name
}

func TestDriftRepairStillRunningIsInFlightNotAFailure(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	appliedFleet(t, h, r)
	r.opWaitMax = 50 * time.Millisecond
	lastSuccess := testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto"))
	require.NotZero(t, lastSuccess)
	name := tamperOne(h)
	release := blockWrites(t, h, "UpdateFunctionConfiguration", name)

	h.now = h.now.Add(2 * time.Hour)
	repairAt := h.now
	require.NoError(t, h.reconcile(r))
	requireNoFailure(t, metrics)
	require.True(t, r.ops().busy(opKeyOf(name)), "修复操作还在跑，结束前不会重复起")
	require.True(t, r.lastVerified.Before(h.now), "还没核对完毕，几分钟后再读回")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Equal(t, float64(repairAt.Unix()), testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")))
	// 操作刚开始、没有卡住：对账本身正常，成功时间照常前进（大批量升级可能连续跑十几分钟，期间一直有在跑的操作）。
	fresh := testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto"))
	require.Equal(t, float64(h.now.Unix()), fresh)
	require.Greater(t, fresh, lastSuccess)

	// 在跑期间再来一轮：同一个函数不会重复起操作。
	ensures := h.cloud.countOps("ensure", name)
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, ensures, h.cloud.countOps("ensure", name))

	// 最早的操作超过 10 分钟仍在跑：说明卡住了，不再刷新成功时间，由 Monitor 的对账陈旧判定发现。
	h.now = repairAt.Add(operationStuckAge + time.Minute)
	stuckBefore := testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto"))
	require.NoError(t, h.reconcile(r))
	requireNoFailure(t, metrics)
	require.Equal(t, stuckBefore, testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")))
	require.Equal(t, float64(repairAt.Unix()), testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")), "pending 起点仍是最早操作的开始时间")
	require.Equal(t, ensures, h.cloud.countOps("ensure", name))

	// 操作结束后恢复：成功时间前进，没有在跑的操作。
	release()
	h.waitOps(r)
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Greater(t, testutil.ToFloat64(metrics.assignmentLastSuccess.WithLabelValues("crypto")), stuckBefore)
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.NotEqual(t, "tampered", h.mustFunction(name).Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
}

func TestUpgradePendingBeforeEnableStillFinishesTheRound(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	// 之前一轮配额超限失败；现在升级操作在跑，EnableAll 返回 errCodeUpgradePending，
	// 管理器随后跳过本 tick 的对账：pending 要更新，遗留的失败原因要清掉。
	_ = r.fail("quota", quotaError())
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	op, err := r.ops().start("fn-a", opKeyUpgrade, false, func(context.Context, *functionOp) error { <-gate; return nil })
	require.NoError(t, err)
	r.roundFailCount = r.failCount // 上一轮已结束
	r.finishRound(errCodeUpgradePending)

	requireNoFailure(t, metrics)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Equal(t, float64(op.startedAt.Unix()), testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")))

	// 其他错误不是等待，不恢复健康。
	_ = r.fail("cloud", errors.New("boom"))
	r.roundFailCount = r.failCount
	r.finishRound(errors.New("enable timers: boom"))
	require.Equal(t, float64(1), failureLabel(metrics, "cloud"))
}

func TestDriftRepairThatFinishedWithFailuresIsStillAFailure(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	appliedFleet(t, h, r)
	name := tamperOne(h)
	h.failWrites("UpdateFunctionConfiguration", name)
	h.now = h.now.Add(2 * time.Hour)
	verifiedBefore := r.lastVerified

	require.Error(t, h.reconcile(r))
	require.Equal(t, float64(1), failureLabel(metrics, "cloud"))
	require.Equal(t, verifiedBefore, r.lastVerified, "有失败项：不更新读回时间，下一轮继续修复")
	require.False(t, r.ops().busy(opKeyOf(name)), "已结束的失败操作不算在跑")
	require.Equal(t, "tampered", h.mustFunction(name).Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
}

// 读云上失败（网关、腾讯云内部错误）按 cloud 报，不归到配额。
func TestCloudReadErrorsStayCloudFailures(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	h.cloud.client.BeforeRead = func(name string, _ tencentscf.FunctionRef) error {
		if name == "GetFunction" {
			return errors.New("InternalError: gateway unavailable")
		}
		return nil
	}
	require.Error(t, r.Reconcile(context.Background()))
	require.Equal(t, float64(1), failureLabel(metrics, "cloud"))
	require.Zero(t, failureLabel(metrics, "quota"))
}

func TestFinishRoundDoesNotClearFailuresRecordedBetweenRounds(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	// 同一 tick 内对账之前的步骤（启用、升级）失败：对账本身没报错，也不能把它当成「本轮无失败」清掉。
	_ = r.fail("package", errors.New("resolve code package: unavailable"))
	require.NoError(t, r.Reconcile(context.Background()))
	require.Equal(t, float64(1), failureLabel(metrics, "package"), "package 是独立的失败原因，不归为 cloud")
	require.Zero(t, failureLabel(metrics, "cloud"))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))

	// 下一轮没有任何失败：恢复。
	require.NoError(t, r.Reconcile(context.Background()))
	requireNoFailure(t, metrics)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
}

func TestEnableAllReadbackFailureIsRecordedAndNotClearedByTheUpgradeWait(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	appliedFleet(t, h, r)
	// 上一轮配额超限失败；本 tick 的启用前读回读不到云上：这是调腾讯云失败，
	// 即使它以“升级等待”收尾，也不能把既有失败当成无失败清掉。
	_ = r.fail("quota", quotaError())
	r.roundFailCount = r.failCount
	h.cloud.client.BeforeRead = func(name string, _ tencentscf.FunctionRef) error {
		if name == "GetFunction" {
			return errors.New("InternalError: context deadline exceeded")
		}
		return nil
	}

	done, err := r.EnableAll(context.Background())
	require.False(t, done)
	require.ErrorIs(t, err, errCodeUpgradePending)
	require.Equal(t, float64(1), failureLabel(metrics, "cloud"))
	require.Zero(t, failureLabel(metrics, "quota"))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
}
