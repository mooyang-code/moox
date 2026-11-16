package client

import (
	"context"
	"os"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/stretchr/testify/require"
)

type topologyInvoker func(context.Context, string, string, any, any) error

func (f topologyInvoker) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

func TestSyncHostDeploymentsSendsCompleteCanonicalInventoryWithLabelAndConsoleSSH(t *testing.T) {
	snapshot := clientConfig(t)
	host := snapshot.ControlHost()
	snapshot.Deployments[host.Name] = []string{"strategy", "monitor", "collector", "admin", "eventbus"}
	calls := 0
	gateway := topologyInvoker(func(_ context.Context, service, method string, req, rsp any) error {
		calls++
		require.Equal(t, "trpc.moox.ops.SysDeploy", service)
		require.Equal(t, "SyncHostDeployments", method)
		request := req.(*pb.SyncHostDeploymentsReq)
		require.Equal(t, host.Name, request.GetHostId())
		require.Equal(t, host.Address, request.GetAddress())
		require.Equal(t, []string{"admin", "collector", "eventbus", "monitor", "strategy"}, request.GetComponentIds())
		require.Equal(t, "控制机", request.GetLabel())
		require.Equal(t, int32(22), request.GetSshPort())
		require.Equal(t, "ubuntu", request.GetSshUser())
		require.Equal(t, "private_key", request.GetSshAuthType())
		require.Equal(t, "PRIVATE KEY BODY\n", request.GetSshSecret())
		rsp.(*pb.SyncHostDeploymentsRsp).RetInfo = &pb.RetInfo{}
		return nil
	})
	require.NoError(t, New(gateway).SyncHostDeployments(t.Context(), snapshot, host.Name))
	require.Equal(t, 1, calls)
	require.Error(t, New(gateway).SyncHostDeployments(t.Context(), snapshot, "unknown-host"))
	require.Equal(t, 1, calls)
}

func TestSyncHostDeploymentsRejectsMissingStatus(t *testing.T) {
	snapshot := clientConfig(t)
	gateway := topologyInvoker(func(_ context.Context, _, _ string, _, rsp any) error { return nil })
	require.ErrorContains(t, New(gateway).SyncHostDeployments(t.Context(), snapshot, snapshot.ControlHost().Name), "setup_response_invalid")
}

func TestSyncHostDeploymentsConsoleSSHRules(t *testing.T) {
	root := t.TempDir()
	key := root + "/id_ed25519"
	require.NoError(t, os.WriteFile(key, []byte("PRIVATE KEY BODY\n"), 0o600))
	cfg, err := topology.Parse([]byte(`[hosts.control]
address = "eventbus.example.test"
ssh = { port = 22, username = "ubuntu", private_key_file = "` + key + `" }

[hosts.storage]
address = "192.0.2.12"
ssh = { port = 22, username = "ubuntu" }

[hosts.factor]
address = "192.168.0.103"
reachability = "outbound"
ssh = { port = 22, username = "builder", private_key_file = "` + key + `" }

[deployments]
control = ["admin", "eventbus"]
storage = []
factor = ["factor-engine", "archive-worker"]
`))
	require.NoError(t, err)
	snapshot := &cfg

	requests := map[string]*pb.SyncHostDeploymentsReq{}
	gateway := topologyInvoker(func(_ context.Context, _, _ string, req, rsp any) error {
		request := req.(*pb.SyncHostDeploymentsReq)
		requests[request.GetHostId()] = request
		rsp.(*pb.SyncHostDeploymentsRsp).RetInfo = &pb.RetInfo{}
		return nil
	})
	for _, id := range []string{"control", "storage", "factor"} {
		require.NoError(t, New(gateway).SyncHostDeployments(t.Context(), snapshot, id))
	}
	require.Equal(t, "private_key", requests["control"].GetSshAuthType())
	require.Equal(t, "PRIVATE KEY BODY\n", requests["control"].GetSshSecret())
	require.Zero(t, requests["storage"].GetSshPort(), "没有配私钥的直达主机不同步 SSH")
	require.Empty(t, requests["storage"].GetSshSecret())
	require.Zero(t, requests["factor"].GetSshPort(), "只能出站的主机不同步 SSH")
	require.Empty(t, requests["factor"].GetSshSecret())
	require.Equal(t, "因子机", requests["factor"].GetLabel())
	require.Equal(t, "outbound", requests["factor"].GetReachability())
	require.Equal(t, []string{"archive-worker", "factor-engine"}, requests["factor"].GetComponentIds())
}
