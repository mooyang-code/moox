package rpc

import (
	"context"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

type doctorDeploymentSource struct{ rows []*adminpb.ServiceDeployment }

func (s doctorDeploymentSource) DesiredDeployments(context.Context) ([]*adminpb.ServiceDeployment, error) {
	return s.rows, nil
}

func TestGetDoctorContextReturnsBoundedFacts(t *testing.T) {
	builder := &monitordoctor.Builder{Deployments: doctorDeploymentSource{rows: []*adminpb.ServiceDeployment{{ServiceName: "moox_monitor", NodeId: "node-a", Status: "active"}}}}
	service := &Service{doctorContext: builder}
	rsp, err := service.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{NodeId: "node-a", ComponentIds: []string{"moox_monitor"}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetExpectedComponents(), 1)
	require.True(t, rsp.GetExpectedComponents()[0].GetExpected())
}

func TestGetDoctorContextRejectsTooManyComponents(t *testing.T) {
	service := &Service{doctorContext: &monitordoctor.Builder{}}
	ids := make([]string, monitorpb.MaxDoctorContextComponents+1)
	rsp, err := service.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{ComponentIds: ids})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
