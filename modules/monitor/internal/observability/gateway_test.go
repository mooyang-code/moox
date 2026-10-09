package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
)

func TestEvaluateGatewayHost(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	cases := []struct {
		name       string
		host       *adminpb.DeployHost
		healthy    bool
		reasonPart string
	}{
		{
			name: "在线且已同步",
			host: &adminpb.DeployHost{HostId: "storage", Status: "enabled", Gateway: &adminpb.HostGatewayStatus{
				State: gatewayOnline, InstanceId: "b", ExpectedHash: "h1", AppliedHash: "h1", LastSeenAt: at(10 * time.Second),
			}},
			healthy: true, reasonPart: "在线",
		},
		{
			name: "网关实例替换不告警",
			host: &adminpb.DeployHost{HostId: "storage", Status: "enabled", Gateway: &adminpb.HostGatewayStatus{
				State: gatewayOnline, InstanceId: "b", PreviousInstanceId: "a", ReplacedAt: at(time.Minute),
				ExpectedHash: "h1", AppliedHash: "h1", LastSeenAt: at(5 * time.Second),
			}},
			healthy: true, reasonPart: "在线",
		},
		{
			name: "网关实例冲突时告警",
			host: &adminpb.DeployHost{HostId: "compute-1", Status: "enabled", Gateway: &adminpb.HostGatewayStatus{
				State: gatewayConflict, InstanceId: "a", ConflictInstanceId: "b", ConflictSeenAt: at(30 * time.Second),
				ExpectedHash: "h1", AppliedHash: "h1", LastSeenAt: at(5 * time.Second),
			}},
			healthy: false, reasonPart: "实例冲突",
		},
		{
			name: "超过 2 分钟没有心跳",
			host: &adminpb.DeployHost{HostId: "compute-1", Status: "enabled", Gateway: &adminpb.HostGatewayStatus{
				State: gatewayOffline, LastSeenAt: at(3 * time.Minute), LastError: "x509: certificate signed by unknown authority",
			}},
			healthy: false, reasonPart: "没有心跳",
		},
		{
			name: "刚登记的主机等待第一次心跳",
			host: &adminpb.DeployHost{HostId: "compute-2", Status: "enabled", CreatedAt: at(30 * time.Second),
				Gateway: &adminpb.HostGatewayStatus{State: gatewayNeverReported}},
			healthy: true, reasonPart: "第一次心跳",
		},
		{
			name: "从未上报心跳超过 2 分钟",
			host: &adminpb.DeployHost{HostId: "compute-2", Status: "enabled", CreatedAt: at(10 * time.Minute),
				Gateway: &adminpb.HostGatewayStatus{State: gatewayNeverReported}},
			healthy: false, reasonPart: "从未上报",
		},
		{
			name: "哈希不一致不到 2 分钟不告警",
			host: &adminpb.DeployHost{HostId: "storage", Status: "enabled", Gateway: &adminpb.HostGatewayStatus{
				State: gatewayOnline, ExpectedHash: "h2", AppliedHash: "h1", OutOfSyncSince: at(time.Minute), LastSeenAt: at(5 * time.Second),
			}},
			healthy: true, reasonPart: "在线",
		},
		{
			name: "哈希不一致超过 2 分钟告警",
			host: &adminpb.DeployHost{HostId: "storage", Status: "enabled", Gateway: &adminpb.HostGatewayStatus{
				State: gatewayOnline, ExpectedHash: "expected-hash-0123456789", AppliedHash: "applied-hash-0123456789",
				OutOfSyncSince: at(5 * time.Minute), LastSeenAt: at(5 * time.Second), LastError: "route validation failed",
			}},
			healthy: false, reasonPart: "未同步",
		},
		{
			name: "停用的主机不检查",
			host: &adminpb.DeployHost{HostId: "compute-1", Status: "disabled", Gateway: &adminpb.HostGatewayStatus{
				State: gatewayOffline, LastSeenAt: at(time.Hour),
			}},
			healthy: true, reasonPart: "已停用",
		},
	}
	for _, tc := range cases {
		got := EvaluateGatewayHost(tc.host, now)
		require.Equal(t, tc.healthy, got.Healthy, "%s: %s", tc.name, got.Reason)
		require.Contains(t, got.Reason, tc.reasonPart, tc.name)
	}
	offline := EvaluateGatewayHost(cases[3].host, now)
	require.Contains(t, offline.Reason, "x509", "失联时带上最近一次错误")
	outOfSync := EvaluateGatewayHost(cases[7].host, now)
	require.Contains(t, outOfSync.Reason, "applied-hash")
	require.NotContains(t, outOfSync.Reason, "0123456789", "哈希只显示前 12 位")
}

type gatewayHostsStub struct {
	hosts []*adminpb.DeployHost
	err   error
}

func (s gatewayHostsStub) Hosts(context.Context) ([]*adminpb.DeployHost, error) {
	return s.hosts, s.err
}

func TestBuilderReportsGatewayHostsAndSourceErrors(t *testing.T) {
	now := time.Now().UTC()
	got, err := (Builder{GatewayHosts: gatewayHostsStub{hosts: []*adminpb.DeployHost{{
		HostId: "storage", Status: "enabled",
		Gateway: &adminpb.HostGatewayStatus{State: gatewayOnline, ExpectedHash: "h", AppliedHash: "h", LastSeenAt: now.Format(time.RFC3339)},
	}}}, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.NoError(t, got.GatewayHostsErr)
	require.Len(t, got.GatewayHosts, 1)
	require.True(t, got.GatewayHosts[0].Healthy)

	got, err = (Builder{GatewayHosts: gatewayHostsStub{err: errors.New("admin unavailable")}}).Build(t.Context(), "")
	require.NoError(t, err, "SysDeploy 读不到时健康概览照常生成")
	require.Error(t, got.GatewayHostsErr)
}
