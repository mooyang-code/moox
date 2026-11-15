package fetchplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/egressbalance"
)

// probeAnswers 让假腾讯云回答出口 IP 探测（egress_probe）：ips 是函数名到此刻出口 IP 的映射。
func probeAnswers(h *planHarness, ips func(name string) string) {
	h.cloud.client.Invoke = func(req tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error) {
		return &tencentscf.InvokeFunctionResponse{RequestID: "probe", ReturnResult: `{"data":{"details":{"public_ip":"` + ips(req.FunctionName) + `"}}}`}, nil
	}
}

// 计划新建了函数后，核对云端函数时发现共用出口 IP：重复的函数先删再按期望内容重建，已生效计划不变；做完之前保持待去重。
func TestPlanReconcilerRecreatesNewFunctionsThatShareAnEgressIP(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	balancer, err := egressbalance.New(egressbalance.Config{MaxPerIP: 1, Rounds: 3})
	require.NoError(t, err)
	r.Egress = balancer
	require.NoError(t, h.reconcile(r))
	require.True(t, balancer.Pending(), "计划新建了函数：标记要做出口 IP 去重")
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	names := h.cloudTimers()
	require.GreaterOrEqual(t, len(names), 2)
	before := map[string]string{}
	for _, name := range names {
		before[name] = h.mustFunction(name).Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"]
	}

	probeAnswers(h, func(string) string { return "1.1.1.1" }) // 全部共用一个 IP
	deletes, creates := h.cloud.client.Count("DeleteFunction"), h.cloud.client.Count("CreateFunction")
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, deletes+len(names)-1, h.cloud.client.Count("DeleteFunction"), "共用 IP 的函数被删除")
	require.Equal(t, creates+len(names)-1, h.cloud.client.Count("CreateFunction"), "删除的函数按期望内容重建")
	require.Zero(t, h.cloud.countOps("delete", names[0]), "每个 IP 保留第一个函数")
	for _, name := range names[1:] {
		require.Equal(t, 1, h.cloud.countOps("delete", name), name)
		function := h.mustFunction(name)
		require.Equal(t, before[name], function.Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"], "重建后的环境是已生效计划那一份")
		require.True(t, function.Timer.Enabled, name)
	}
	require.Nil(t, h.open(), "计划本身没有变化")
	require.True(t, balancer.Pending(), "还没确认重建后的 IP 是否分开")
	require.True(t, r.lastVerified.Before(h.now), "几分钟后再探测下一轮")

	// 重建后 IP 已经分开：下一轮探测确认去重完成，不再删建。
	probeAnswers(h, func(name string) string { return "ip-" + name })
	deletes = h.cloud.client.Count("DeleteFunction")
	h.now = h.now.Add(planVerifyRetry)
	require.NoError(t, h.reconcile(r))
	require.False(t, balancer.Pending())
	require.Equal(t, deletes, h.cloud.client.Count("DeleteFunction"))
}

// 云端函数被删后按已生效计划重建：重建出来的函数也要做一次出口 IP 去重（重新初始化的路径）。
func TestPlanReconcilerArmsEgressBalanceWhenMissingFunctionsAreRebuilt(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)

	balancer, err := egressbalance.New(egressbalance.Config{})
	require.NoError(t, err)
	r.Egress = balancer // 计划生效之后才配置：创建函数时没有标记
	require.False(t, balancer.Pending())
	missing := h.cloudTimers()[0]
	h.remove(missing)
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.True(t, h.timerEnabled(missing), "缺失的函数被重建")
	require.True(t, balancer.Pending(), "函数被删后重建：标记要做出口 IP 去重")
	require.True(t, r.lastVerified.Before(h.now), "去重还没做完：几分钟后再核对，而不是等一小时")
}
