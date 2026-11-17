package command

import (
	"context"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type cleanupGatewayWire struct {
	collectorpb.UnimplementedCollectMgr
	directorypb.UnimplementedDirectory
}

func (cleanupGatewayWire) GetDirectory(context.Context, *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	// 目录只有主机与部署，CLI 按内嵌的服务目录自己展开出 CollectMgr 在 control 上。
	return &directorypb.GetDirectoryRsp{Changed: true, Version: "v1",
		Hosts:       []*directorypb.DirectoryHost{{HostId: "control", Address: "unused.example.test", Reachability: "direct", Enabled: true}},
		Deployments: []*directorypb.DirectoryDeployment{{HostId: "control", ComponentId: "admin", Enabled: true}, {HostId: "control", ComponentId: "collector", Enabled: true}}}, nil
}
func (cleanupGatewayWire) ListCloudAccounts(context.Context, *collectorpb.ListCloudAccountsReq) (*collectorpb.ListCloudAccountsRsp, error) {
	return &collectorpb.ListCloudAccountsRsp{RetInfo: &commonpb.RetInfo{}, Accounts: []*collectorpb.CloudAccount{{AccountId: "account-1", Provider: "tencent"}}}, nil
}

// 命令自己的网关：一次调用被取消不影响之后的调用（收尾调用用 WithoutCancel 的上下文），关闭之后调用失败。
func TestCommandGatewaySurvivesCanceledOperationAndThenCloses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	wire := &cleanupGatewayWire{}
	collectorpb.RegisterCollectMgrService(service, wire)
	directorypb.RegisterDirectoryService(service, wire)
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".config", "moox")
	require.NoError(t, os.MkdirAll(config, 0700))
	knownHosts := filepath.Join(config, "known_hosts")
	require.NoError(t, os.WriteFile(knownHosts, nil, 0600))
	snapshot := &topology.Config{}
	testfixture.SetHost(snapshot, testfixture.GatewaySSH(t, "control", listener.Addr().String(), knownHosts), "admin", "eventbus")
	require.NoError(t, os.WriteFile(filepath.Join(config, "moox-cli.key"), []byte(gatewayauth.EncodeSigningKey(authtest.Identity("moox-cli").Key)+"\n"), 0600))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gateway, err := openCommandGateway(ctx, "", snapshot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	client := &adminclient.Client{Gateway: gateway}
	cancel()
	_, err = client.ListCloudAccounts(ctx, "")
	require.Error(t, err)
	accounts, err := client.ListCloudAccounts(context.WithoutCancel(ctx), "")
	require.NoError(t, err)
	require.Equal(t, "account-1", accounts[0].AccountID)
	require.NoError(t, gateway.Close())
	_, err = client.ListCloudAccounts(context.WithoutCancel(ctx), "")
	require.Error(t, err)
}
