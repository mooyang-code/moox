package test

import (
	"context"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/placement"
	"github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type doctorContextDeployments struct{ rows []*adminpb.ComponentPlacement }

func (s doctorContextDeployments) Snapshot(context.Context) (placement.Snapshot, error) {
	catalog, _ := servicecatalog.LoadEmbedded()
	hosts := map[string]bool{}
	snapshot := placement.Snapshot{Catalog: catalog, Placements: s.rows}
	for _, row := range s.rows {
		if !hosts[row.GetHostId()] {
			hosts[row.GetHostId()] = true
			snapshot.Hosts = append(snapshot.Hosts, &adminpb.DeploymentHost{HostId: row.GetHostId(), Address: row.GetHostId() + ".example.test", Status: "enabled"})
		}
	}
	return snapshot, nil
}

func TestDoctorContextEndToEndDisabledAndDeferredFacts(t *testing.T) {
	builder := &monitordoctor.Builder{Deployments: doctorContextDeployments{rows: []*adminpb.ComponentPlacement{
		{ComponentId: "factor-mgr", HostId: "node-a", Status: "disabled"},
		{ComponentId: "storage-primary", HostId: "node-a", Status: "enabled"},
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
