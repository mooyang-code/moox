package doctor

import (
	"context"
	"errors"
	"testing"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNilClientFailsClosed(t *testing.T) {
	var client *Client
	_, err := client.GetHealthOverview(context.Background(), "")
	require.ErrorContains(t, err, "不可用")
	_, err = client.ListDeployments(context.Background(), "node-a")
	require.ErrorContains(t, err, "unavailable")
}

type doctorGatewayStub struct {
	response *monitorpb.GetHealthOverviewRsp
	err      error
}

func (s doctorGatewayStub) Invoke(_ context.Context, service, method string, _ any, rsp any) error {
	if service != "trpc.moox.monitor.MonitorMgr" || method != "GetHealthOverview" {
		return errors.New("unexpected service")
	}
	if s.err != nil {
		return s.err
	}
	proto.Merge(rsp.(*monitorpb.GetHealthOverviewRsp), s.response)
	return nil
}

func TestDoctorUsesNativeGatewayAndRequiresStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *monitorpb.GetHealthOverviewRsp
		err      error
		ok       bool
	}{
		{"success", &monitorpb.GetHealthOverviewRsp{RetInfo: &commonpb.RetInfo{}, Overview: &monitorpb.HealthOverview{TopologyKnown: true}}, nil, true},
		{"missing status", &monitorpb.GetHealthOverviewRsp{}, nil, false},
		{"missing overview", &monitorpb.GetHealthOverviewRsp{RetInfo: &commonpb.RetInfo{}}, nil, false},
		{"business failure", &monitorpb.GetHealthOverviewRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION}}, nil, false},
		{"transport", nil, errors.New("native transport failed"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(doctorGatewayStub{response: test.response, err: test.err}).GetHealthOverview(t.Context(), "")
			if test.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	_, err := New(nil).GetHealthOverview(t.Context(), "")
	require.Error(t, err)
}
