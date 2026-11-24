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

// 一次调用带上 moox.toml 的全部主机与部署（含启停状态）；组件按 ID 排序，主机级组件由 Admin 补。
func TestSyncTopologySendsEveryHostWithStatusAndConsoleSSH(t *testing.T) {
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
enabled = false
ssh = { port = 22, username = "builder", private_key_file = "` + key + `" }

[deployments]
control = ["strategy", "admin", "eventbus"]
storage = []
factor = ["factor-engine", "archive-worker"]

[deployments_disabled]
control = ["strategy"]
`))
	require.NoError(t, err)
	calls := 0
	hosts := map[string]*pb.SyncTopologyHost{}
	gateway := topologyInvoker(func(_ context.Context, service, method string, req, rsp any) error {
		calls++
		require.Equal(t, "trpc.moox.ops.SysDeploy", service)
		require.Equal(t, "SyncTopology", method)
		for _, host := range req.(*pb.SyncTopologyReq).GetHosts() {
			hosts[host.GetHostId()] = host
		}
		rsp.(*pb.SyncTopologyRsp).RetInfo = &pb.RetInfo{}
		return nil
	})
	require.NoError(t, New(gateway).SyncTopology(t.Context(), &cfg))
	require.Equal(t, 1, calls)
	require.Len(t, hosts, 3)

	control := hosts["control"]
	require.True(t, control.GetEnabled())
	require.Equal(t, "控制机", control.GetLabel())
	require.Equal(t, []string{"admin", "eventbus", "strategy"}, control.GetComponentIds())
	require.Equal(t, []string{"strategy"}, control.GetDisabledComponentIds())
	require.Equal(t, int32(22), control.GetSshPort())
	require.Equal(t, "private_key", control.GetSshAuthType())
	require.Equal(t, "PRIVATE KEY BODY\n", control.GetSshSecret())

	require.True(t, hosts["storage"].GetEnabled())
	require.Zero(t, hosts["storage"].GetSshPort(), "没有配私钥的直达主机不同步 SSH")

	factor := hosts["factor"]
	require.False(t, factor.GetEnabled())
	require.Zero(t, factor.GetSshPort(), "只能出站的主机不同步 SSH")
	require.Equal(t, "outbound", factor.GetReachability())
	require.Equal(t, []string{"archive-worker", "factor-engine"}, factor.GetComponentIds())
}

func TestSyncTopologyRejectsMissingStatus(t *testing.T) {
	gateway := topologyInvoker(func(_ context.Context, _, _ string, _, rsp any) error { return nil })
	require.ErrorContains(t, New(gateway).SyncTopology(t.Context(), clientConfig(t)), "setup_response_invalid")
}
