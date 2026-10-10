package command

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type cleanupGatewayWire struct {
	cloudnodepb.UnimplementedCloudNodeMgr
	directorypb.UnimplementedDirectory
}

func (cleanupGatewayWire) GetDirectory(context.Context, *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	directory := servicecatalog.Directory{Hosts: map[string]servicecatalog.DirectoryHost{"control": {Address: "unused.example.test"}}, Services: map[string][]string{"trpc.moox.cloudnode.CloudNodeMgr": {"control"}}}
	version, err := directory.VersionHash()
	return &directorypb.GetDirectoryRsp{Changed: true, Version: version, Hosts: map[string]*directorypb.DirectoryHost{"control": {Address: "unused.example.test"}}, Services: map[string]*directorypb.ServiceHosts{"trpc.moox.cloudnode.CloudNodeMgr": {HostIds: []string{"control"}}}}, err
}
func (cleanupGatewayWire) GetNodeBatchChange(_ context.Context, req *cloudnodepb.GetNodeBatchChangeReq) (*cloudnodepb.GetNodeBatchChangeRsp, error) {
	return &cloudnodepb.GetNodeBatchChangeRsp{RetInfo: &cloudnodepb.RetInfo{}, Job: &cloudnodepb.NodeBatchSummary{JobId: req.GetJobId()}}, nil
}

func TestCommandGatewaySurvivesCanceledOperationForFencedCleanupAndThenCloses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	wire := &cleanupGatewayWire{}
	cloudnodepb.RegisterCloudNodeMgrService(service, wire)
	directorypb.RegisterDirectoryService(service, wire)
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".config", "moox")
	require.NoError(t, os.MkdirAll(config, 0700))
	knownHosts := filepath.Join(config, "known_hosts")
	require.NoError(t, os.WriteFile(knownHosts, nil, 0600))
	snapshot := &setupconfig.Snapshot{}
	testfixture.SetHost(&snapshot.Manifest, testfixture.GatewaySSH(t, "control", listener.Addr().String(), knownHosts), "admin", "console-proxy", "web-host", "eventbus")
	require.NoError(t, os.WriteFile(filepath.Join(config, "gateway-client.yaml"), []byte("caller: moox-cli\nkey_id: assigned-command-key\nkey_file: caller-moox-cli.key\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(config, "caller-moox-cli.key"), []byte("command-fixture-secret-at-least-32-bytes"), 0600))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gateway, err := openCommandGateway(ctx, "", snapshot)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	client := &adminclient.Client{Gateway: gateway}
	cancel()
	_, err = client.GetNodeBatchChange(ctx, "job-1")
	require.Error(t, err)
	status, err := client.GetNodeBatchChange(context.WithoutCancel(ctx), "job-1")
	require.NoError(t, err)
	require.Equal(t, "job-1", status.Job.JobID)
	require.NoError(t, gateway.Close())
	_, err = client.GetNodeBatchChange(context.WithoutCancel(ctx), "job-1")
	require.Error(t, err)
}
