package test

import (
	"context"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type doctorContextDeployments struct{ rows []*adminpb.ServiceDeployment }

func (s doctorContextDeployments) DesiredDeployments(context.Context) ([]*adminpb.ServiceDeployment, error) {
	return s.rows, nil
}

func TestDoctorContextEndToEndDisabledAndDeferredFacts(t *testing.T) {
	builder := &monitordoctor.Builder{Deployments: doctorContextDeployments{rows: []*adminpb.ServiceDeployment{
		{ServiceName: "moox_factor", NodeId: "node-a", Status: "disabled"},
		{ServiceName: "storage-primary", NodeId: "node-a", Status: "active"},
	}}}
	service := rpc.New(nil, rpc.Options{DoctorContext: builder})
	rsp, err := service.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{NodeId: "node-a", ComponentIds: []string{"moox_factor", "storage_primary"}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.LessOrEqual(t, proto.Size(rsp), monitorpb.MaxDoctorContextBytes)
	byID := map[string]*monitorpb.DoctorExpectedComponent{}
	for _, component := range rsp.GetExpectedComponents() {
		byID[component.GetComponentId()] = component
	}
	require.False(t, byID["moox_factor"].GetExpected())
	require.Equal(t, "deferred", byID["storage_primary"].GetFunctionalObservability())
	require.Empty(t, rsp.GetWatermarks(), "Storage must not receive a synthesized success watermark")
}

func TestDoctorContextEndToEndRejectsRequestLimits(t *testing.T) {
	service := rpc.New(nil, rpc.Options{DoctorContext: &monitordoctor.Builder{}})
	rsp, err := service.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{PipelineIds: make([]string, monitorpb.MaxDoctorContextPipelines+1)})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
