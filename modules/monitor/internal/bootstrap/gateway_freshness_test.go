package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

type gatewayHostsSource struct {
	hosts []*adminpb.DeployHost
	err   error
}

func (s *gatewayHostsSource) Hosts(context.Context) ([]*adminpb.DeployHost, error) {
	return s.hosts, s.err
}

func TestBusinessFreshnessReporterAlertsOnHostGatewayAndKeepsStateWhenSysDeployIsDown(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repositories := manager.Repositories()
	now := time.Now().UTC().Truncate(time.Second)
	source := &gatewayHostsSource{hosts: []*adminpb.DeployHost{
		{HostId: "storage", Status: "enabled", Gateway: &adminpb.HostGatewayStatus{State: "online", ExpectedHash: "h", AppliedHash: "h", LastSeenAt: now.Format(time.RFC3339)}},
		{HostId: "compute-1", Status: "enabled", Gateway: &adminpb.HostGatewayStatus{State: "offline", LastSeenAt: now.Add(-10 * time.Minute).Format(time.RFC3339)}},
	}}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{GatewayHosts: source, Checks: repositories.Checks, Results: repositories.Results, Now: func() time.Time { return now }}, repositories, nil)
	require.NoError(t, run(t.Context()))

	latest := func(checkID string) domain.CheckResult {
		t.Helper()
		results, err := repositories.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, checkID, 1)
		require.NoError(t, err)
		require.Len(t, results, 1, checkID)
		return results[0]
	}
	require.True(t, latest("host_gateway:storage").Success)
	offline := latest("host_gateway:compute-1")
	require.False(t, offline.Success)
	require.Contains(t, offline.ErrorMessage, "没有心跳")
	check, err := repositories.Checks.Get(t.Context(), monmetrics.InternalMetricSpaceID, "host_gateway:compute-1")
	require.NoError(t, err)
	require.Equal(t, "主机网关（compute-1）· 心跳与路由同步", check.Name)
	rule, err := repositories.Alerts.GetRule(t.Context(), monmetrics.InternalMetricSpaceID, "default:host_gateway:compute-1")
	require.NoError(t, err, "主机网关检查带默认告警规则")
	require.Equal(t, 1, rule.FailureThreshold)

	// SysDeploy 读不到时，主机网关检查保留上一次的失败，不能被当作已恢复。
	source.err = errors.New("admin unavailable")
	require.NoError(t, run(t.Context()))
	require.False(t, latest("host_gateway:compute-1").Success)

	// 主机删除后，检查按「不再检查」恢复。
	source.err, source.hosts = nil, source.hosts[:1]
	require.NoError(t, run(t.Context()))
	resolved := latest("host_gateway:compute-1")
	require.True(t, resolved.Success)
	require.Contains(t, resolved.ErrorMessage, "不再检查")
}
