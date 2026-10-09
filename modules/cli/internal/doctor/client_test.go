package doctor

import (
	"context"
	"testing"

	"errors"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNilClientFailsClosed(t *testing.T) {
	var client *Client
	_, err := client.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{})
	require.ErrorContains(t, err, "unavailable")
	_, err = client.ListDeployments(context.Background(), "node-a")
	require.ErrorContains(t, err, "unavailable")
}

type doctorGatewayStub struct {
	response *monitorpb.GetDoctorContextRsp
	err      error
}

func (s doctorGatewayStub) Invoke(_ context.Context, service, method string, req, rsp any) error {
	if service != "trpc.moox.monitor.MonitorMgr" || method != "GetDoctorContext" {
		return errors.New("unexpected service")
	}
	if s.err != nil {
		return s.err
	}
	proto.Merge(rsp.(*monitorpb.GetDoctorContextRsp), s.response)
	return nil
}
func TestDoctorUsesNativeGatewayAndRequiresStatus(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *monitorpb.GetDoctorContextRsp
		err      error
		ok       bool
	}{
		{"success", &monitorpb.GetDoctorContextRsp{RetInfo: &commonpb.RetInfo{}, ManifestChecksum: "fixture"}, nil, true},
		{"missing status", &monitorpb.GetDoctorContextRsp{}, nil, false},
		{"business failure", &monitorpb.GetDoctorContextRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION}}, nil, false},
		{"transport", nil, errors.New("native transport failed"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(doctorGatewayStub{response: test.response, err: test.err}).GetDoctorContext(t.Context(), &monitorpb.GetDoctorContextReq{})
			if test.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	_, err := New(nil).GetDoctorContext(t.Context(), &monitorpb.GetDoctorContextReq{})
	require.Error(t, err)
}
