package doctor

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
)

type deploymentsInvoker struct {
	rows       []*pb.ComponentDeployment
	hostStatus string
	more       bool
	calls      int
}

func (g *deploymentsInvoker) Invoke(_ context.Context, service, method string, req, rsp any) error {
	if service != "trpc.moox.ops.SysDeploy" {
		panic("unexpected service")
	}
	g.calls++
	switch method {
	case "ListHosts":
		response := rsp.(*pb.ListDeploymentHostsRsp)
		response.RetInfo = &pb.RetInfo{}
		response.Hosts = []*pb.DeploymentHost{{HostId: "control", Address: "control.example.test", Status: g.hostStatus}}
	case "ListDeployments":
		response := rsp.(*pb.ListDeploymentsRsp)
		response.RetInfo = &pb.RetInfo{}
		response.Deployments = g.rows
		response.PageResult = &pb.PageResult{HasMore: g.more}
	default:
		panic("Doctor must only use v2 inventory")
	}
	return nil
}

func TestDoctorDeploymentsUseCatalogHealthAndHostEnablement(t *testing.T) {
	gateway := &deploymentsInvoker{hostStatus: "disabled", rows: []*pb.ComponentDeployment{{HostId: "control", ComponentId: "admin", Status: "enabled"}}}
	rows, err := New(gateway).ListDeployments(t.Context(), "control")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "admin", rows[0].ComponentID)
	require.Equal(t, "disabled", rows[0].Status)
	require.Contains(t, rows[0].HealthAddress, ":11010")
	require.Equal(t, 2, gateway.calls)
}

func TestDoctorDeploymentsRejectInconsistentAndUnboundedInventory(t *testing.T) {
	for _, rows := range [][]*pb.ComponentDeployment{
		{{HostId: "other", ComponentId: "admin", Status: "enabled"}},
		{{HostId: "control", ComponentId: "unknown", Status: "enabled"}},
		{{HostId: "control", ComponentId: "admin", Status: "active"}},
		{{HostId: "control", ComponentId: "admin", Status: "enabled"}, {HostId: "control", ComponentId: "admin", Status: "enabled"}},
	} {
		_, err := New(&deploymentsInvoker{hostStatus: "enabled", rows: rows}).ListDeployments(t.Context(), "control")
		require.Error(t, err)
	}
	gateway := &deploymentsInvoker{hostStatus: "enabled", more: true}
	_, err := New(gateway).ListDeployments(t.Context(), "control")
	require.ErrorContains(t, err, "exceeds")
	require.Equal(t, 6, gateway.calls)
}
