package command

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type doctorAlertsFakeGateway struct {
	service, method string
	req             proto.Message
	rsp             proto.Message
	closed          bool
}

func (g *doctorAlertsFakeGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	g.service, g.method, g.req = service, method, req.(proto.Message)
	if g.rsp != nil {
		proto.Merge(rsp.(proto.Message), g.rsp)
	}
	return nil
}
func (g *doctorAlertsFakeGateway) Close() error { g.closed = true; return nil }
func (g *doctorAlertsFakeGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, nil
}

func runDoctorAlerts(t *testing.T, overview *monitorpb.HealthOverview, args ...string) (string, error, *doctorAlertsFakeGateway) {
	t.Helper()
	gateway := &doctorAlertsFakeGateway{rsp: &monitorpb.GetHealthOverviewRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Overview: overview}}
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) { return gateway, nil }
	t.Cleanup(func() { openCommandGateway = previous })
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
	out, err, gateway := runDoctorAlerts(t, &monitorpb.HealthOverview{GeneratedAt: time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC).UnixMilli()}, "--space-id", "crypto")
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
