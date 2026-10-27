package marketfetch

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
)

// 数据集全部是推送、云端没有任何计划函数：代码包被清掉（云函数 GC）也不能挡住计划的推进。
func TestStreamOnlyPlanDoesNotNeedACodePackage(t *testing.T) {
	h := newIngestHarness(t, modeStream, "1m", "1h")
	h.r.Deploy.PackageID, h.r.Deploy.PackageName = "", "moox-collector-crypto-plan"
	require.Empty(t, h.cloud.packages, "没有任何可用代码包")

	h.settle(true)
	content := h.applied()
	require.Len(t, content.Functions, 1)
	require.Empty(t, h.cloud.created)
	require.Empty(t, h.r.packageID())
}

// 进程重启后没有缓存的包 ID，之后的每一轮对账照常推进计划变更（新增标的进入推送订阅）。
func TestStreamOnlyPlanKeepsApplyingChangesAfterARestartWithoutAPackage(t *testing.T) {
	h := newIngestHarness(t, modeStream, "1m")
	h.r.Deploy.PackageID, h.r.Deploy.PackageName = "", "moox-collector-crypto-plan"
	h.settle(true)
	before := h.applied()

	h.r = h.newReconciler(modeStream) // 模拟重启：新对账器
	h.r.Deploy.PackageID, h.r.Deploy.PackageName = "", "moox-collector-crypto-plan"
	h.setSubjects(61)
	h.settle(true)
	after := h.applied()
	require.NotEqual(t, before.Functions[streamItem].New, after.Functions[streamItem].New)
	plan := after.FinalPlan(after.Functions[streamItem])
	require.Len(t, plan.Datasets[0].Copies[0].Subjects, 61)
}

// 云端已经有这个空间的计划函数：它们要升级到配置的代码包，解析不到就拦住，不下发新环境。
func TestMissingPackageStillBlocksWhenCloudFunctionsExist(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.Deploy.PackageID, r.Deploy.PackageName = "", "moox-collector-crypto-plan"
	h.cloud.nodes = map[string]scfinvoker.FunctionInstance{}
	name := r.fleetPrefix() + "spot-001"
	h.cloud.nodes[name] = scfinvoker.FunctionInstance{InstanceID: name, PackageID: "pkg-old"}

	err := h.reconcile(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "code package")
	require.True(t, errors.Is(err, errNoCodePackage))
	require.Empty(t, h.cloud.patches, "不下发新环境")
	require.Nil(t, h.open(), "也不创建新计划")
}

// 需要创建云函数时仍然明确报错：不用空包 ID 创建，失败原因归为 package。
func TestCreatingFunctionsWithoutAPackageFailsAsPackage(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.Deploy.PackageID, r.Deploy.PackageName = "", "moox-collector-crypto-plan"
	err := h.reconcile(r)
	require.ErrorIs(t, err, errNoCodePackage)
	require.Empty(t, h.cloud.created)
}

// 解析不到代码包的警告按间隔记录，不会每轮刷屏。
func TestMissingPackageWarningIsThrottled(t *testing.T) {
	h := newIngestHarness(t, modeStream, "1m")
	h.r.Deploy.PackageID, h.r.Deploy.PackageName = "", "moox-collector-crypto-plan"
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

// 创建中、还没标记就绪的预留同样拦住：它们随后就绪时要升级到配置的代码包。
func TestMissingPackageAlsoBlocksWhileFunctionsAreStillBeingCreated(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.Deploy.PackageID, r.Deploy.PackageName = "", "moox-collector-crypto-plan"
	name := r.fleetPrefix() + "spot-001"
	h.cloud.unready = map[string]scfinvoker.FunctionInstance{name: {InstanceID: name}}

	err := h.reconcile(r)
	require.ErrorIs(t, err, errNoCodePackage)
	require.Empty(t, h.cloud.patches, "不下发新环境")
}
