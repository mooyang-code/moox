package marketfetch

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/egressbalance"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
)

// probeCloud 是带出口探测的假云端：每次探测按 ips 里设定的当前值返回。
type probeCloud struct {
	*fakePlanCloud
	ips map[string]string
}

func (c *probeCloud) ProbeEgressIP(_ context.Context, _, instanceID string) (string, error) {
	return c.ips[instanceID], nil
}

// 计划新建了函数后，核对云端函数时发现共用出口 IP：重复的函数走删除再重建，已生效计划不变；做完之后不再探测。
func TestPlanReconcilerRecreatesNewFunctionsThatShareAnEgressIP(t *testing.T) {
	h := newPlanHarness(t, 60)
	cloud := &probeCloud{fakePlanCloud: h.cloud, ips: map[string]string{}}
	r := h.reconciler()
	r.Cloud = cloud
	balancer, err := egressbalance.New(egressbalance.Config{MaxPerIP: 1, Rounds: 3})
	require.NoError(t, err)
	r.Egress = balancer
	require.NoError(t, h.reconcile(r))
	require.True(t, balancer.Pending(), "计划新建了函数：标记要做出口 IP 去重")
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
	require.True(t, balancer.Pending(), "还没确认重建后的 IP 是否分开")
}

// 云端函数被删后按已生效计划重建：重建出来的函数也要做一次出口 IP 去重（重新初始化的路径）。
func TestPlanReconcilerArmsEgressBalanceWhenMissingFunctionsAreRebuilt(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())

	balancer, err := egressbalance.New(egressbalance.Config{})
	require.NoError(t, err)
	r.Egress = balancer // 计划生效之后才配置：创建函数时没有标记
	require.False(t, balancer.Pending())
	for name := range h.cloud.nodes {
		delete(h.cloud.nodes, name)
		break
	}
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.True(t, balancer.Pending(), "函数被删后重建：标记要做出口 IP 去重")
	require.True(t, r.lastVerified.Before(h.now), "去重还没做完：几分钟后再核对，而不是等一小时")
}
