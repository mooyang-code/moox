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

type doctorContextPlacements struct{ rows []*adminpb.DeployPlacement }

func (s doctorContextPlacements) Hosts(context.Context) ([]*adminpb.DeployHost, error) {
	return []*adminpb.DeployHost{{HostId: "node-a", Address: "203.0.113.10", Status: "enabled"}}, nil
}

func (s doctorContextPlacements) Placements(context.Context) ([]*adminpb.DeployPlacement, error) {
	return s.rows, nil
}

func TestDoctorContextEndToEndDisabledAndDeferredFacts(t *testing.T) {
	builder := &monitordoctor.Builder{Placements: doctorContextPlacements{rows: []*adminpb.DeployPlacement{
		{HostId: "node-a", ComponentId: "factor-mgr", Status: "disabled"},
		{HostId: "node-a", ComponentId: "storage-primary", Status: "enabled"},
	}}}
	service := rpc.New(nil, rpc.Options{DoctorContext: builder})
	rsp, err := service.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{NodeId: "node-a", ComponentIds: []string{"factor-mgr", "storage-primary"}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.LessOrEqual(t, proto.Size(rsp), monitorpb.MaxDoctorContextBytes)
	byID := map[string]*monitorpb.DoctorExpectedComponent{}
	for _, component := range rsp.GetExpectedComponents() {
		byID[component.GetComponentId()] = component
	}
	require.False(t, byID["factor-mgr"].GetExpected())
	require.Equal(t, "deferred", byID["storage-primary"].GetFunctionalObservability())
	require.Empty(t, rsp.GetWatermarks(), "Storage must not receive a synthesized success watermark")
}

func TestDoctorContextEndToEndRejectsRequestLimits(t *testing.T) {
	service := rpc.New(nil, rpc.Options{DoctorContext: &monitordoctor.Builder{}})
	rsp, err := service.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{HealthCheckIds: make([]string, monitorpb.MaxDoctorContextHealthChecks+1)})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
