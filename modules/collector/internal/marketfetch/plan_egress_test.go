package marketfetch

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
)

func TestEgressDuplicatesKeepsTheFirstFunctionOfEachIP(t *testing.T) {
	ips := map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "2.2.2.2", "f-003": "1.1.1.1", "f-004": "2.2.2.2", "f-005": "3.3.3.3"}
	require.Equal(t, []string{"f-001", "f-003", "f-004"}, egressDuplicates(ips, 1))
	require.Equal(t, []string{"f-003"}, egressDuplicates(ips, 2), "每个 IP 最多保留 2 个")
	require.Empty(t, egressDuplicates(map[string]string{"a": "1.1.1.1", "b": "2.2.2.2"}, 1))
	require.Equal(t, egressDuplicates(ips, 1), egressDuplicates(ips, 0), "非正数按 1 处理")
}

// probeCloud 是带出口探测的假云端：每次探测按 ips 里设定的当前值返回。
type probeCloud struct {
	*fakePlanCloud
	ips    map[string]string
	failOn string
	probes int
}

func (c *probeCloud) ProbeEgressIP(_ context.Context, _, instanceID string) (string, error) {
	c.probes++
	if instanceID == c.failOn {
		return "", errors.New("probe failed")
	}
	return c.ips[instanceID], nil
}

func egressReconciler(cloud *probeCloud, balance *EgressBalance) (*PlanReconciler, []string, map[string]scfinvoker.FunctionInstance) {
	reconciler := &PlanReconciler{SpaceID: "stockcn", Cloud: cloud, EgressBalance: balance, EgressIdle: func(time.Time) bool { return true }, egressPending: true}
	names := make([]string, 0, len(cloud.ips))
	byName := map[string]scfinvoker.FunctionInstance{}
	for _, name := range []string{"f-000", "f-001", "f-002", "f-003"} {
		names = append(names, name)
		byName[name] = scfinvoker.FunctionInstance{InstanceID: name}
	}
	return reconciler, names, byName
}

func TestEgressRecreateDeletesSharedIPsUntilTheyAreSpread(t *testing.T) {
	cloud := &probeCloud{fakePlanCloud: newFakePlanCloud(), ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "1.1.1.1", "f-003": "2.2.2.2"}}
	reconciler, names, byName := egressReconciler(cloud, &EgressBalance{MaxPerIP: 1, Rounds: 5})
	ctx := context.Background()

	require.Equal(t, []string{"f-001", "f-002"}, reconciler.egressRecreate(ctx, names, byName), "共用 IP 的函数除第一个外都重建")
	require.True(t, reconciler.egressPending)

	// 重建后一个拿到了新 IP，另一个仍然重复。
	cloud.ips["f-001"], cloud.ips["f-002"] = "3.3.3.3", "1.1.1.1"
	require.Equal(t, []string{"f-002"}, reconciler.egressRecreate(ctx, names, byName))

	cloud.ips["f-002"] = "4.4.4.4"
	require.Empty(t, reconciler.egressRecreate(ctx, names, byName))
	require.False(t, reconciler.egressPending, "每个函数的 IP 都不同：结束")
	probes := cloud.probes
	require.Empty(t, reconciler.egressRecreate(ctx, names, byName))
	require.Equal(t, probes, cloud.probes, "结束后不再探测")
}

func TestEgressRecreateStopsWhenTheAddressPoolIsTooSmall(t *testing.T) {
	cloud := &probeCloud{fakePlanCloud: newFakePlanCloud(), ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "2.2.2.2", "f-003": "2.2.2.2"}}
	reconciler, names, byName := egressReconciler(cloud, &EgressBalance{MaxPerIP: 1, Rounds: 5})
	ctx := context.Background()
	require.NotEmpty(t, reconciler.egressRecreate(ctx, names, byName), "第一轮：不同 IP 数 2，是目前最好")
	require.NotEmpty(t, reconciler.egressRecreate(ctx, names, byName), "第二轮没有改善，还可以再试一轮")
	require.Empty(t, reconciler.egressRecreate(ctx, names, byName), "连续两轮没有改善：地址池里的 IP 不够，停止")
	require.False(t, reconciler.egressPending)
}

func TestEgressRecreateStopsAfterTheConfiguredRounds(t *testing.T) {
	cloud := &probeCloud{fakePlanCloud: newFakePlanCloud(), ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "1.1.1.1", "f-003": "1.1.1.1"}}
	reconciler, names, byName := egressReconciler(cloud, &EgressBalance{MaxPerIP: 1, Rounds: 1})
	ctx := context.Background()
	require.NotEmpty(t, reconciler.egressRecreate(ctx, names, byName))
	cloud.ips["f-001"] = "2.2.2.2" // 有改善，但已经用完轮数
	require.Empty(t, reconciler.egressRecreate(ctx, names, byName))
	require.False(t, reconciler.egressPending)
}

func TestEgressRecreateWaitsForIdleTimeAndCompleteProbes(t *testing.T) {
	cloud := &probeCloud{fakePlanCloud: newFakePlanCloud(), ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "2.2.2.2", "f-003": "3.3.3.3"}}
	reconciler, names, byName := egressReconciler(cloud, &EgressBalance{MaxPerIP: 1, Rounds: 5})
	ctx := context.Background()

	reconciler.EgressIdle = func(time.Time) bool { return false }
	require.Empty(t, reconciler.egressRecreate(ctx, names, byName), "交易时段不删除重建")
	require.Zero(t, cloud.probes, "也不探测")

	reconciler.EgressIdle = func(time.Time) bool { return true }
	cloud.failOn = "f-003"
	require.Empty(t, reconciler.egressRecreate(ctx, names, byName), "有函数探测失败：这一轮不做决定")
	require.True(t, reconciler.egressPending)
	require.Zero(t, reconciler.egressRounds, "探测失败不算一轮")

	cloud.failOn = ""
	require.Equal(t, []string{"f-001"}, reconciler.egressRecreate(ctx, names, byName))

	reconciler.EgressBalance = nil
	probes := cloud.probes
	require.Empty(t, reconciler.egressRecreate(ctx, names, byName), "没配置就不做")
	require.Equal(t, probes, cloud.probes)
}

func TestStockCNIdleForRecreateIsOutsideTheSession(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	at := func(h, m int) time.Time { return time.Date(2026, 11, 6, h, m, 0, 0, shanghai) }
	require.True(t, StockCNIdleForRecreate(at(15, 10)))
	require.True(t, StockCNIdleForRecreate(at(20, 0)))
	require.True(t, StockCNIdleForRecreate(at(8, 59)))
	require.False(t, StockCNIdleForRecreate(at(9, 0)))
	require.False(t, StockCNIdleForRecreate(at(11, 45)), "午休也不动：上午最后一期和下午开盘都要用")
	require.False(t, StockCNIdleForRecreate(at(15, 9)))
	require.True(t, StockCNIdleForRecreate(at(7, 30).UTC()), "按北京时间换算，与传入的时区无关")
}

// 计划新建了函数后，核对云端函数时发现共用出口 IP：重复的函数走删除再重建，已生效计划不变；做完之后不再探测。
func TestPlanReconcilerRecreatesNewFunctionsThatShareAnEgressIP(t *testing.T) {
	h := newPlanHarness(t, 60)
	cloud := &probeCloud{fakePlanCloud: h.cloud, ips: map[string]string{}}
	r := h.reconciler()
	r.Cloud = cloud
	r.EgressBalance = &EgressBalance{MaxPerIP: 1, Rounds: 3}
	r.EgressIdle = func(time.Time) bool { return true }
	require.NoError(t, h.reconcile(r))
	require.True(t, r.egressPending, "计划新建了函数：标记要做出口 IP 去重")
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	require.GreaterOrEqual(t, len(h.cloud.nodes), 2)
	// 先让已有的配置漂移修复完：出口 IP 去重只在核对没有其他问题时才做。
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))

	names := make([]string, 0, len(h.cloud.nodes))
	for name := range h.cloud.nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	h.now = h.now.Add(2 * time.Hour) // 下一轮核对的时刻；读回时间要和它同步，否则算过期
	for _, name := range names {
		cloud.ips[name] = "1.1.1.1" // 全部共用一个 IP
		// 让云端读回与已生效计划完全一致（没有漂移）。
		patch := h.cloud.patches[name]
		env := patch.GetManagedEnvironment()
		h.cloud.nodes[name] = scfinvoker.FunctionInstance{InstanceID: name, Metadata: map[string]any{
			"assignment_hash": env["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"], "timer_actual_enabled": true, "timer_available_status": "Available",
			"timer_actual_cron": patch.GetTimerCron(), "timer_actual_qualifier": timerTriggerQualifier, "timer_actual_message": timerTriggerMessage,
			"access_address": env["MOOX_ACCESS_ADDRESS"], "access_id": env["MOOX_ACCESS_ID"], "access_key_id": env["MOOX_CALLER_KEY_ID"],
			"timer_last_readback_at": h.now.UTC().Format(time.RFC3339Nano),
		}}
	}

	created := len(h.cloud.created)
	require.NoError(t, h.reconcile(r))
	for _, name := range names[1:] {
		require.Contains(t, h.cloud.deleted, name, "共用 IP 的函数被删除")
	}
	require.NotContains(t, h.cloud.deleted, names[0], "每个 IP 保留第一个函数")
	require.Equal(t, created+len(names)-1, len(h.cloud.created), "删除的函数按已生效计划重建")
	require.Nil(t, h.open(), "计划本身没有变化")
	require.True(t, r.egressPending, "还没确认重建后的 IP 是否分开")
}

func TestEgressRecreateOnlyRunsAfterFunctionsWereCreated(t *testing.T) {
	cloud := &probeCloud{fakePlanCloud: newFakePlanCloud(), ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "2.2.2.2", "f-003": "3.3.3.3"}}
	reconciler, names, byName := egressReconciler(cloud, &EgressBalance{MaxPerIP: 1, Rounds: 5})
	reconciler.egressPending = false
	require.Empty(t, reconciler.egressRecreate(context.Background(), names, byName), "没有新建函数就不探测、不重建")
	require.Zero(t, cloud.probes)
}
