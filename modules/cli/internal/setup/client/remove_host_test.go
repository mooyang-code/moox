package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

func TestRemoveRegisteredHostClearsDeploymentsDeletesHostAndSSHRecords(t *testing.T) {
	var calls []string
	gateway := topologyInvoker(func(_ context.Context, service, method string, req, rsp any) error {
		calls = append(calls, service+"/"+method)
		switch service + "/" + method {
		case "trpc.moox.ops.SysDeploy/ListHosts":
			require.Equal(t, "compute-1", req.(*pb.ListDeploymentHostsReq).GetHostId())
			rsp.(*pb.ListDeploymentHostsRsp).RetInfo = &pb.RetInfo{}
			rsp.(*pb.ListDeploymentHostsRsp).Hosts = []*pb.DeploymentHost{{HostId: "compute-1", Address: "198.51.100.7", Region: "ap-hongkong", Reachability: "direct"}}
		case "trpc.moox.ops.SysDeploy/ListDeployments":
			rsp.(*pb.ListDeploymentsRsp).RetInfo = &pb.RetInfo{}
			rsp.(*pb.ListDeploymentsRsp).Deployments = []*pb.ComponentDeployment{{HostId: "compute-1", ComponentId: "trade"}, {HostId: "compute-1", ComponentId: "access"}}
		case "trpc.moox.ops.Ssh/ListHosts":
			rsp.(*pb.ListHostsRsp).RetInfo = &pb.RetInfo{}
			rsp.(*pb.ListHostsRsp).Hosts = []*pb.SSHHost{{Id: 7, Name: "compute-1", Password: "recognizable-ssh-password"}, {Id: 8, Name: "compute-10"}}
		case "trpc.moox.ops.SysDeploy/SyncHostDeployments":
			request := req.(*pb.SyncHostDeploymentsReq)
			require.Equal(t, "compute-1", request.GetHostId())
			require.Equal(t, "198.51.100.7", request.GetAddress())
			require.Empty(t, request.GetComponentIds(), "先清空业务组件部署")
			rsp.(*pb.SyncHostDeploymentsRsp).RetInfo = &pb.RetInfo{}
		case "trpc.moox.ops.SysDeploy/DeleteHost":
			require.Equal(t, "compute-1", req.(*pb.DeleteDeploymentHostReq).GetHostId())
			rsp.(*pb.DeleteDeploymentHostRsp).RetInfo = &pb.RetInfo{}
		case "trpc.moox.ops.Ssh/DeleteHost":
			require.Equal(t, int32(7), req.(*pb.DeleteHostReq).GetId(), "只删除同名记录")
			rsp.(*pb.DeleteHostRsp).RetInfo = &pb.RetInfo{}
		default:
			t.Fatalf("unexpected call %s/%s", service, method)
		}
		return nil
	})
	client := New(gateway)
	host, found, err := client.RegisteredHostByID(t.Context(), "compute-1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, RegisteredHost{HostID: "compute-1", Address: "198.51.100.7", Components: []string{"access", "trade"}, SSHRecords: 1}, host)

	calls = nil
	require.NoError(t, client.RemoveRegisteredHost(t.Context(), host))
	require.Equal(t, []string{
		"trpc.moox.ops.SysDeploy/ListHosts", "trpc.moox.ops.SysDeploy/SyncHostDeployments", "trpc.moox.ops.SysDeploy/DeleteHost",
		"trpc.moox.ops.Ssh/ListHosts", "trpc.moox.ops.Ssh/DeleteHost",
	}, calls)
}

func TestRemoveRegisteredHostSurfacesAdminRefusal(t *testing.T) {
	gateway := topologyInvoker(func(_ context.Context, service, method string, req, rsp any) error {
		switch method {
		case "ListHosts":
			if service == "trpc.moox.ops.SysDeploy" {
				rsp.(*pb.ListDeploymentHostsRsp).RetInfo = &pb.RetInfo{}
				rsp.(*pb.ListDeploymentHostsRsp).Hosts = []*pb.DeploymentHost{{HostId: "old", Address: "198.51.100.8"}}
			}
		case "SyncHostDeployments":
			rsp.(*pb.SyncHostDeploymentsRsp).RetInfo = &pb.RetInfo{}
		case "DeleteHost":
			rsp.(*pb.DeleteDeploymentHostRsp).RetInfo = &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "invalid topology: host still has component deployments"}
		}
		return nil
	})
	err := New(gateway).RemoveRegisteredHost(t.Context(), RegisteredHost{HostID: "old"})
	require.ErrorContains(t, err, "host still has component deployments")

	_, found, err := New(gateway).RegisteredHostByID(t.Context(), "missing")
	require.NoError(t, err)
	require.False(t, found)
}
