package command

import (
	"context"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type cleanupGatewayWire struct {
	collectorpb.UnimplementedCollectMgr
	directorypb.UnimplementedDirectory
}

func (cleanupGatewayWire) GetDirectory(context.Context, *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	directory := servicecatalog.Directory{Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "unused.example.test"}}, Services: map[string][]string{"trpc.moox.collector.CollectMgr": {"control"}}}
	version, err := directory.VersionHash()
	return &directorypb.GetDirectoryRsp{Changed: true, Version: version, Hosts: map[string]*directorypb.DirectoryHost{"control": {Address: "unused.example.test"}}, Services: map[string]*directorypb.ServiceHosts{"trpc.moox.collector.CollectMgr": {HostIds: []string{"control"}}}}, err
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
	require.NoError(t, os.WriteFile(filepath.Join(config, "gateway-client.yaml"), []byte("caller: moox-cli\nkey_id: assigned-command-key\nkey_file: caller-moox-cli.key\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(config, "caller-moox-cli.key"), []byte("command-fixture-secret-at-least-32-bytes"), 0600))
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
