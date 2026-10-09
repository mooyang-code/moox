package rpc

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type doctorDeploymentSource struct{ rows []*adminpb.ServiceDeployment }

func (s doctorDeploymentSource) DesiredDeployments(context.Context) ([]*adminpb.ServiceDeployment, error) {
	return s.rows, nil
}

func TestGetDoctorContextReturnsBoundedFacts(t *testing.T) {
	builder := &monitordoctor.Builder{Deployments: doctorDeploymentSource{rows: []*adminpb.ServiceDeployment{{ServiceName: "monitor", NodeId: "node-a", Status: "active"}}}}
	service := &Service{doctorContext: builder}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	rpc := server.New(server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTransport(transport.NewServerTransport()))
	monitorpb.RegisterMonitorMgrService(rpc, service)
	done := make(chan error, 1)
	go func() { done <- rpc.Serve() }()
	t.Cleanup(func() {
		_ = rpc.Close(nil)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Monitor native listener did not stop")
		}
	})
	for _, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		proxy := monitorpb.NewMonitorMgrClientProxy(client.WithTarget("ip://"+listener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithSerializationType(serialization), client.WithTimeout(3*time.Second), client.WithTransport(transport.NewClientTransport()), client.WithDisableConnectionPool())
		rsp, err := proxy.GetDoctorContext(context.Background(), &monitorpb.GetDoctorContextReq{NodeId: "node-a", ComponentIds: []string{"monitor"}})
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		require.Len(t, rsp.GetExpectedComponents(), 1)
		require.True(t, rsp.GetExpectedComponents()[0].GetExpected())
	}
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
