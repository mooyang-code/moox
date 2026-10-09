package rpc

import (
	"context"
	"strings"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type doctorPlacementSource struct{ rows []*adminpb.DeployPlacement }

func (s doctorPlacementSource) Hosts(context.Context) ([]*adminpb.DeployHost, error) {
	return []*adminpb.DeployHost{{HostId: "node-a", Address: "203.0.113.10", Status: "enabled"}}, nil
}

func (s doctorPlacementSource) Placements(context.Context) ([]*adminpb.DeployPlacement, error) {
	return s.rows, nil
}

func TestGetDoctorContextReturnsBoundedFacts(t *testing.T) {
	builder := &monitordoctor.Builder{Placements: doctorPlacementSource{rows: []*adminpb.DeployPlacement{{HostId: "node-a", ComponentId: "monitor", Status: "enabled"}}}}
	service := &Service{doctorContext: builder}
	rsp, err := service.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{NodeId: "node-a", ComponentIds: []string{"monitor"}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetExpectedComponents(), 1)
	require.True(t, rsp.GetExpectedComponents()[0].GetExpected())
}

func TestDoctorContextUsesHealthCheckWireNames(t *testing.T) {
	request := &monitorpb.GetDoctorContextReq{
		NodeId:         "node-a",
		HealthCheckIds: []string{"monitor-metrics"},
	}
	require.Equal(t, []string{"monitor-metrics"}, request.GetHealthCheckIds())

	wire := contextToPB(monitordoctor.Context{Watermarks: []monitordoctor.Watermark{{
		Module: "monitor", Stage: "ingest", HealthCheckID: "monitor-metrics",
	}}})
	require.Equal(t, "monitor-metrics", wire.GetWatermarks()[0].GetHealthCheckId())
}

func TestGetDoctorContextRejectsTooManyComponents(t *testing.T) {
	service := &Service{doctorContext: &monitordoctor.Builder{}}
	ids := make([]string, monitorpb.MaxDoctorContextComponents+1)
	rsp, err := service.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{ComponentIds: ids})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestDoctorContextRejectsOversizedResponse(t *testing.T) {
	rsp := &monitorpb.GetDoctorContextRsp{MissingObservations: []*monitorpb.DoctorObservation{{DetailsJson: string(make([]byte, monitorpb.MaxDoctorContextBytes+1))}}}
	require.Error(t, validateDoctorResponseSize(rsp))
}

func TestDoctorContextRejectsOversizedJSONEncoding(t *testing.T) {
	rsp := &monitorpb.GetDoctorContextRsp{MissingObservations: []*monitorpb.DoctorObservation{{DetailsJson: strings.Repeat(`\`, monitorpb.MaxDoctorContextBytes/2+1)}}}
	require.Less(t, proto.Size(rsp), monitorpb.MaxDoctorContextBytes)
	require.Error(t, validateDoctorResponseSize(rsp))
}
