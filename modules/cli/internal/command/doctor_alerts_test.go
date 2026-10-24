package command

import (
	"bytes"
	"testing"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/stretchr/testify/require"
)

func runDoctorAlerts(t *testing.T, overview *monitorpb.HealthOverview, args ...string) (string, error, *archiveFakeGateway) {
	t.Helper()
	gateway := &archiveFakeGateway{rsp: &monitorpb.GetHealthOverviewRsp{RetInfo: ok(), Overview: overview}}
	useArchiveGateway(t, gateway)
	cmd := newDoctorAlertsCommand(doctorCommandDeps{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	return out.String(), err, gateway
}

// doctor alerts 经网关读取 Monitor 健康概览，健康时输出空清单、退出码 0；有严重问题时仍输出清单并以退出码 2 结束。
func TestDoctorAlertsReadsOverviewAndSetsExitCode(t *testing.T) {
	out, err, gateway := runDoctorAlerts(t, &monitorpb.HealthOverview{GeneratedAt: "2026-10-24T03:00:00Z"}, "--space", "crypto")
	require.NoError(t, err)
	require.Equal(t, "trpc.moox.monitor.MonitorMgr", gateway.service)
	require.Equal(t, "GetHealthOverview", gateway.method)
	require.Equal(t, "crypto", gateway.req.(*monitorpb.GetHealthOverviewReq).GetSpaceId())
	require.Contains(t, out, `"conclusion": "HEALTHY"`)
	require.True(t, gateway.closed)

	out, err, _ = runDoctorAlerts(t, &monitorpb.HealthOverview{
		Hosts: []*monitorpb.HealthHost{{HostId: "overseas", Status: "down", Reason: "主机采集代理停止上报"}},
	}, "--format", "text")
	var exit doctorExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 2, exit.ExitCode())
	require.Contains(t, out, "[critical/host] 主机 overseas")
}

func TestDoctorAlertsRejectsUnknownFormat(t *testing.T) {
	_, err, gateway := runDoctorAlerts(t, &monitorpb.HealthOverview{}, "--format", "yaml")
	require.Error(t, err)
	require.Empty(t, gateway.method, "格式不合法时不连网关")
}
