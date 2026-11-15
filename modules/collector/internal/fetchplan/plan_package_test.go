package fetchplan

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/cloudtest"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
)

// 数据集全部是推送、云端没有任何计划函数：代码包被清掉也不能挡住计划的推进。
func TestStreamOnlyPlanDoesNotNeedACodePackage(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m", "1h")
	h.cloud.setPackages()

	h.settle(true)
	content := h.applied()
	require.Len(t, content.Functions, 1)
	require.Empty(t, h.cloudTimers())
	require.Empty(t, h.r.packageID())
}

// 进程重启后没有缓存的包 ID，之后的每一轮对账照常推进计划变更（新增标的进入推送订阅）。
func TestStreamOnlyPlanKeepsApplyingChangesAfterARestartWithoutAPackage(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m")
	h.cloud.setPackages()
	h.settle(true)
	before := h.applied()

	h.r = h.newReconciler() // 模拟重启：新对账器
	h.setSubjects(61)
	h.settle(true)
	after := h.applied()
	require.NotEqual(t, before.Functions[streamItem].New, after.Functions[streamItem].New)
	plan := after.FinalPlan(after.Functions[streamItem])
	require.Len(t, plan.Datasets[0].Copies[0].Subjects, 61)
}

// seedSpaceFunction 在云上放一个名字符合本空间前缀的函数。
func seedSpaceFunction(h *planHarness, r *PlanReconciler, status string) {
	name := r.fleetPrefix() + "spot-nanjing-001"
	h.scf.Seed(tencentscf.FunctionRef{Region: "ap-nanjing", Namespace: "default", FunctionName: name}, &cloudtest.Function{
		Info: tencentscf.FunctionInfo{Status: status, Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "pkg-old"}},
	})
}

// 云端已经有这个空间的计划函数：它们要升级到最新的代码包，解析不到就拦住，不下发新环境。
func TestMissingPackageStillBlocksWhenCloudFunctionsExist(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.cloud.setPackages()
	seedSpaceFunction(h, r, "Active")

	err := h.reconcile(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "code package")
	require.True(t, errors.Is(err, errNoCodePackage))
	require.Empty(t, h.scf.Writes(), "不改云上任何东西")
	require.Nil(t, h.open(), "也不创建新计划")
}

// 需要创建云函数时仍然明确报错：不用空包 ID 创建，失败原因归为 package。
func TestCreatingFunctionsWithoutAPackageFailsAsPackage(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	metrics := roundMetrics(h, r)
	h.cloud.setPackages()
	err := h.reconcile(r)
	require.ErrorIs(t, err, errNoCodePackage)
	require.Zero(t, h.cloud.client.Count("CreateFunction"))
	require.Equal(t, float64(1), failureLabel(metrics, "package"))
}

// 解析不到代码包的警告按间隔记录，不会每轮刷屏。
func TestMissingPackageWarningIsThrottled(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m")
	h.cloud.setPackages()
	h.r.refreshPackageIfAvailable(t.Context())
	first := h.r.packageWarnedAt
	require.False(t, first.IsZero())
	h.now = h.now.Add(time.Minute)
	h.r.refreshPackageIfAvailable(t.Context())
	require.Equal(t, first, h.r.packageWarnedAt)
	h.now = h.now.Add(planPackageWarnInterval)
	h.r.refreshPackageIfAvailable(t.Context())
	require.True(t, h.r.packageWarnedAt.After(first))
}

// 云上有一个还在创建中的本空间函数同样拦住：它建好之后同样要跑最新的代码包。
func TestMissingPackageAlsoBlocksWhileFunctionsAreStillBeingCreated(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.cloud.setPackages()
	seedSpaceFunction(h, r, "Creating")

	err := h.reconcile(r)
	require.ErrorIs(t, err, errNoCodePackage)
	require.Empty(t, h.scf.Writes(), "不下发新环境")
}
