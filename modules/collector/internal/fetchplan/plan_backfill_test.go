package fetchplan

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/cloudtest"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
)

// backfillHarness 搭好一个只用补采函数的对账器：代码包按名称取最新（pkg-v1），一次调用里最多等 50 毫秒。
func backfillHarness(t *testing.T) (*planHarness, *PlanReconciler, string) {
	t.Helper()
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.opWaitMax = 50 * time.Millisecond
	h.cloud.setPackages("pkg-v1")
	return h, r, BackfillFunctionName("crypto", "spot", "ap-nanjing")
}

// 首次使用：补采函数被建出来，没有定时器、不带采集计划，预留并发 5 份内存，代码包是最新的。
func TestBackfillFunctionIsCreatedOnFirstUse(t *testing.T) {
	h, r, name := backfillHarness(t)
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	function := h.mustFunction(name)
	require.Nil(t, function.Timer, "补采函数没有定时器")
	require.Equal(t, "backfill", function.Info.Environment["MOOX_MARKET_FETCH_MODE"])
	require.NotContains(t, function.Info.Environment, marketfetch.PlanEnvName)
	// 补采函数不带计划的单次请求超时：取数用数据源配置里的 request_timeout（美股补齐一块是一次约 20 天的请求，要比实时的 3 秒长）。
	require.NotContains(t, function.Info.Environment, "MOOX_FETCH_REQUEST_TIMEOUT_MS")
	require.Equal(t, int64(DefaultBackfillReservedConcurrency*64), function.ReservedMem)
	require.Equal(t, "pkg-v1", function.Info.Environment["MOOX_CODE_PACKAGE_ID"])
	require.Equal(t, "packages/pkg-v1.zip", function.COSObject)
	require.Equal(t, "storage", function.Info.Environment["MOOX_ENTRY_HOST"])
	require.Equal(t, 1, h.cloud.client.Count("CreateFunction"))
}

// 创建要好几分钟：第一次调用起了操作、等到上限就返回“仍在创建”；创建中再调不重复建；建好之后返回函数名。
func TestBackfillCreateStillRunningIsNotResubmitted(t *testing.T) {
	h, r, name := backfillHarness(t)
	release := blockWrites(t, h, "CreateFunction", name)

	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillFunctionPending)
	require.Equal(t, 1, h.cloud.client.Count("CreateFunction"))

	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillFunctionPending)
	require.Equal(t, 1, h.cloud.client.Count("CreateFunction"), "创建中重复调用不重复建")
	require.Equal(t, 1, h.cloud.countOps("ensure", name))

	release()
	h.waitOps(r)
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.Equal(t, 1, h.cloud.client.Count("CreateFunction"))
	require.Equal(t, 1, h.cloud.countOps("ensure", name), "做完的操作只取结果")
}

// 代码包变了：补采函数重新 Ensure，代码与包 ID 换成新包；之后不再重复。
func TestBackfillFunctionIsUpgradedWhenThePackageChanges(t *testing.T) {
	h, r, name := backfillHarness(t)
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)

	h.cloud.setPackages("pkg-v1", "pkg-v2")
	h.now = h.now.Add(time.Minute) // 超过代码包的刷新间隔
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	function := h.mustFunction(name)
	require.Equal(t, "pkg-v2", function.Info.Environment["MOOX_CODE_PACKAGE_ID"])
	require.Equal(t, "packages/pkg-v2.zip", function.COSObject)
	require.Equal(t, 1, h.cloud.client.Count("CreateFunction"), "换代码不重建")
	require.Equal(t, 1, h.cloud.client.Count("UpdateFunctionCode"))

	ensures := h.cloud.countOps("ensure", name)
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, ensures, h.cloud.countOps("ensure", name))
}

// 操作在一次调用的等待上限内做完：当次就返回函数名，不留在跑的操作。
func TestBackfillJobThatFinishesInTimeReleasesTheLease(t *testing.T) {
	h, r, name := backfillHarness(t)
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.False(t, r.ops().busy(opKeyOf(name)))
	require.Nil(t, r.ops().current(name), "结果已被取走")
	_, ready := r.backfillRefs[name]
	require.True(t, ready, "补采调用能按名字找到函数")
	require.Equal(t, 1, h.cloud.countOps("ensure", name))
}

// 建函数失败：返回这次的错误（不是“仍在创建”），下一次调用重新 Ensure。
func TestBackfillSubmitFailureReleasesTheLease(t *testing.T) {
	h, r, name := backfillHarness(t)
	recover := h.failWrites("CreateFunction", name)
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.Error(t, err)
	require.NotErrorIs(t, err, errBackfillFunctionPending)
	require.False(t, r.ops().busy(opKeyOf(name)))
	_, exists := h.cloudFunction(name)
	require.False(t, exists)

	recover()
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.Equal(t, "backfill", h.mustFunction(name).Info.Environment["MOOX_MARKET_FETCH_MODE"])
}

// 换代码还在跑：返回“仍在创建”，期间不重复起；做完后返回函数名。
func TestBackfillUpgradeStillRunningKeepsTheLease(t *testing.T) {
	h, r, name := backfillHarness(t)
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	h.cloud.setPackages("pkg-v1", "pkg-v2")
	h.now = h.now.Add(time.Minute)
	release := blockWrites(t, h, "UpdateFunctionCode", name)

	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillFunctionPending)
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillFunctionPending)
	require.Equal(t, 1, h.cloud.client.Count("UpdateFunctionCode"), "在跑期间不重复起")

	release()
	h.waitOps(r)
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.Equal(t, "pkg-v2", h.mustFunction(name).Info.Environment["MOOX_CODE_PACKAGE_ID"])
}

// 建失败遗留的补采函数（云上 CreateFailed）：Ensure 先删掉、等删完，再建成，不需要人工清理。
func TestBackfillStaleReservationCleanupStillRunningKeepsTheLease(t *testing.T) {
	h, r, name := backfillHarness(t)
	h.scf.Seed(scfRef(name), &cloudtest.Function{Info: tencentscf.FunctionInfo{Status: "CreateFailed", Environment: map[string]string{"MOOX_CREATE_RESERVATION_ID": "old"}}})
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.Equal(t, 1, h.cloud.client.Count("DeleteFunction"))
	function := h.mustFunction(name)
	require.Equal(t, "Active", function.Info.Status)
	require.NotContains(t, function.Info.Environment, "MOOX_CREATE_RESERVATION_ID")
}

// 补采函数与实时对账互不阻塞：实时函数上有操作在跑（例如计划推进），补采函数照样建得出来；反过来补采函数在建也不挡实时函数。
func TestBackfillLeaseHeldByRealtimeIsRetriedLaterWithoutTrackingAJob(t *testing.T) {
	_, r, name := backfillHarness(t)
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	timer := planFunctionNames(t, 60)[0]
	_, err := r.ops().start(opKeyOf(timer), planOpKey(1), true, func(context.Context, *functionOp) error { <-gate; return nil })
	require.NoError(t, err)

	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.True(t, r.ops().busy(opKeyOf(timer)), "实时函数的操作不受影响")
}

// 暂停期间（系统暂停、恢复追赶）补采函数照常创建：它没有定时器，不算“可能打开定时器”的操作。
func TestBackfillFunctionIsCreatedWhileTheSpaceIsPausing(t *testing.T) {
	h, r, name := backfillHarness(t)
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.Equal(t, 1, h.cloud.client.Count("CreateFunction"))
}

// 历史补齐按地域轮流派发：没配 backfill_region 时用市场白名单里的全部地域，配了就只用它；没配地域的市场没有。
func TestBackfillRegionsFollowTheMarketWhitelist(t *testing.T) {
	_, r, _ := backfillHarness(t)
	r.Options.Markets = map[string][]string{"spot": {"ap-tokyo", "ap-singapore", "ap-tokyo"}}
	require.Equal(t, []string{"ap-singapore", "ap-tokyo"}, r.BackfillRegions("spot"), "升序去重")
	require.Nil(t, r.BackfillRegions("swap"), "没配地域的市场没有")

	r.Options.BackfillRegion = "ap-nanjing"
	require.Equal(t, []string{"ap-nanjing"}, r.BackfillRegions("spot"), "配了 backfill_region 只用它，补采不占定时函数的出口地址")

	planned, err := r.plannedNames(context.Background())
	require.NoError(t, err)
	require.Contains(t, planned, refKey(r.backfillRef("spot", "ap-nanjing")))
	require.NotContains(t, planned, refKey(r.backfillRef("spot", "ap-tokyo")))
}

// 指定地域建补采函数：函数名带地域，地域必须是补采地域之一；预留并发可配。
func TestEnsureBackfillFunctionInCreatesTheRegionalFunction(t *testing.T) {
	h, r, _ := backfillHarness(t)
	r.Options.Markets = map[string][]string{"spot": {"ap-nanjing", "ap-guangzhou"}}
	r.Options.BackfillReservedConcurrency = 8
	r.Deploy.Networks["ap-guangzhou"] = r.Deploy.Networks["ap-nanjing"]

	_, err := r.EnsureBackfillFunctionIn(context.Background(), "spot", "ap-tokyo")
	require.Error(t, err, "不是补采地域")

	name, err := r.EnsureBackfillFunctionIn(context.Background(), "spot", "ap-guangzhou")
	require.NoError(t, err)
	require.Equal(t, BackfillFunctionName("crypto", "spot", "ap-guangzhou"), name)
	function := h.mustFunction(name)
	require.Equal(t, int64(8*64), function.ReservedMem)
	require.Equal(t, 8, r.BackfillReservedConcurrency())
	require.Equal(t, DefaultBackfillReservedConcurrency, (&PlanReconciler{}).BackfillReservedConcurrency())
}

// 慢云端操作等待在 backfillMu 锁外进行：一个地域的补采函数正在创建等待（可达 50s）时，
// 同一空间里已就绪的其他地域必须立即返回，同一地域的并发调用也立即返回 pending，不得被 backfillMu 阻塞。
func TestEnsureBackfillFunctionWaitDoesNotHoldBackfillMu(t *testing.T) {
	h, r, nanjingName := backfillHarness(t)
	r.Options.Markets = map[string][]string{"spot": {"ap-nanjing", "ap-guangzhou"}}
	r.Deploy.Networks["ap-guangzhou"] = r.Deploy.Networks["ap-nanjing"]
	r.opWaitMax = 5 * time.Second

	// 先让 ap-nanjing 就绪。
	got, err := r.EnsureBackfillFunctionIn(context.Background(), "spot", "ap-nanjing")
	require.NoError(t, err)
	require.Equal(t, nanjingName, got)

	// 阻塞 ap-guangzhou 的 CreateFunction，并发触发慢创建等待。
	guangzhouName := BackfillFunctionName("crypto", "spot", "ap-guangzhou")
	release := blockWrites(t, h, "CreateFunction", guangzhouName)

	type callResult struct {
		name string
		err  error
	}
	slowDone := make(chan callResult, 1)
	go func() {
		name, callErr := r.EnsureBackfillFunctionIn(context.Background(), "spot", "ap-guangzhou")
		slowDone <- callResult{name: name, err: callErr}
	}()

	// 等到 ap-guangzhou 的操作已实际进入云端 CreateFunction（即原调用已释放 backfillMu 进入 wait）。
	require.Eventually(t, func() bool {
		return h.cloud.client.Count("CreateFunction") >= 2
	}, time.Second, 5*time.Millisecond)

	// 在 ap-guangzhou 阻塞等待期间，已就绪的 ap-nanjing 必须立即命中缓存返回，绝不被 5s 等待堵住。
	started := time.Now()
	fastName, fastErr := r.EnsureBackfillFunctionIn(context.Background(), "spot", "ap-nanjing")
	require.NoError(t, fastErr)
	require.Equal(t, nanjingName, fastName)
	require.Less(t, time.Since(started), 500*time.Millisecond, "已就绪地域不应被另一地域的云等待阻塞")

	// 对正在创建的 ap-guangzhou 的并发调用也应立即返回 errBackfillFunctionPending，不重复建也不堵在锁上。
	started = time.Now()
	_, pendingErr := r.EnsureBackfillFunctionIn(context.Background(), "spot", "ap-guangzhou")
	require.ErrorIs(t, pendingErr, errBackfillFunctionPending)
	require.Less(t, time.Since(started), 500*time.Millisecond)

	// 放行云端创建后，慢调用成功返回，并可被并发读取。
	release()
	res := <-slowDone
	require.NoError(t, res.err)
	require.Equal(t, guangzhouName, res.name)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name, callErr := r.EnsureBackfillFunctionIn(context.Background(), "spot", "ap-guangzhou")
			require.NoError(t, callErr)
			require.Equal(t, guangzhouName, name)
		}()
	}
	wg.Wait()
	require.Equal(t, 2, h.cloud.client.Count("CreateFunction"), "两地域各建一次，无重复创建")
}
