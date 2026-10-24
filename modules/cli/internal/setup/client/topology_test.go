package client

import (
	"context"
	"os"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

type topologyInvoker func(context.Context, string, string, any, any) error

func (f topologyInvoker) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

func TestSyncHostDeploymentsSendsCompleteCanonicalInventoryWithLabelAndConsoleSSH(t *testing.T) {
	snapshot := clientSnapshot(t)
	host := snapshot.Manifest.ControlHost()
	snapshot.Manifest.Deployments[host.Name] = []string{"web-host", "monitor", "console-proxy", "admin", "eventbus"}
	calls := 0
	gateway := topologyInvoker(func(_ context.Context, service, method string, req, rsp any) error {
		calls++
		require.Equal(t, "trpc.moox.ops.SysDeploy", service)
		require.Equal(t, "SyncHostDeployments", method)
		request := req.(*pb.SyncHostDeploymentsReq)
		require.Equal(t, host.Name, request.GetHostId())
		require.Equal(t, host.Address, request.GetAddress())
		require.Equal(t, []string{"admin", "console-proxy", "eventbus", "monitor", "web-host"}, request.GetComponentIds())
		require.Equal(t, "控制机", request.GetLabel())
		require.Equal(t, int32(22), request.GetSshPort())
		require.Equal(t, "ubuntu", request.GetSshUser())
		require.Equal(t, "password", request.GetSshAuthType())
		require.Equal(t, "recognizable-control-password", request.GetSshSecret())
		rsp.(*pb.SyncHostDeploymentsRsp).RetInfo = &pb.RetInfo{}
		return nil
	})
	require.NoError(t, New(gateway).SyncHostDeployments(t.Context(), snapshot, host.Name))
	require.Equal(t, 1, calls)
	require.Error(t, New(gateway).SyncHostDeployments(t.Context(), snapshot, "unknown-host"))
	require.Equal(t, 1, calls)
}

func TestSyncHostDeploymentsRejectsChangedInputAndMissingStatus(t *testing.T) {
	snapshot, path := clientSnapshotWithPath(t)
	host := snapshot.Manifest.ControlHost()
	gateway := topologyInvoker(func(_ context.Context, _, _ string, _, rsp any) error { return nil })
	require.ErrorContains(t, New(gateway).SyncHostDeployments(t.Context(), snapshot, host.Name), "setup_response_invalid")
	require.NoError(t, os.WriteFile(path, []byte("changed"), 0600))
	require.ErrorContains(t, New(gateway).SyncHostDeployments(t.Context(), snapshot, host.Name), "config_changed")
}

func TestSyncHostDeploymentsConsoleSSHRules(t *testing.T) {
	root := t.TempDir()
	key := root + "/id_ed25519"
	require.NoError(t, os.WriteFile(key, []byte("PRIVATE KEY BODY\n"), 0o600))
	body := `[admin]
username = "admin"
password = "recognizable-admin-password"

[tencent_cloud]
secret_id = "recognizable-secret-id"
secret_key = "recognizable-secret-key"

[eventbus]
port = 4222
tls_enabled = true

[hosts.control]
address = "eventbus.example.test"
ssh = { username = "ubuntu", private_key_file = "` + key + `", private_key_passphrase = "phrase" }

[hosts.storage]
address = "192.0.2.12"
ssh = { username = "ubuntu" }

[hosts.factor]
address = "192.168.0.103"
reachability = "outbound"
ssh = { username = "builder", password = "outbound-password" }

[deployments]
control = ["admin", "console-proxy", "web-host", "eventbus"]
storage = []
factor = ["factor-engine", "archive-worker"]
`
	path := root + "/moox.toml"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	snapshot, err := setupconfig.Load(path, root)
	require.NoError(t, err)

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
	require.Equal(t, "phrase", requests["control"].GetSshPassphrase())
	require.Zero(t, requests["storage"].GetSshPort(), "没有密码或私钥的直达主机不同步 SSH")
	require.Empty(t, requests["storage"].GetSshSecret())
	require.Zero(t, requests["factor"].GetSshPort(), "只能出站的主机不同步 SSH")
	require.Empty(t, requests["factor"].GetSshSecret())
	require.Equal(t, "因子机", requests["factor"].GetLabel())
	require.Equal(t, "outbound", requests["factor"].GetReachability())
	require.Equal(t, []string{"archive-worker", "factor-engine"}, requests["factor"].GetComponentIds())
}
