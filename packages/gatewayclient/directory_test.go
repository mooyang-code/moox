package gatewayclient

import (
	"context"
	"net"
	"testing"
	"time"

	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type directoryServiceFunc func(context.Context, *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error)

func (f directoryServiceFunc) GetDirectory(ctx context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	return f(ctx, req)
}

// directoryResponse 是本机网关下发的目录：只有主机与部署。
func directoryResponse(update DirectoryUpdate) *directorypb.GetDirectoryRsp {
	rsp := &directorypb.GetDirectoryRsp{Changed: true, Version: update.Version}
	for _, host := range update.Hosts {
		rsp.Hosts = append(rsp.Hosts, &directorypb.DirectoryHost{HostId: host.ID, Address: host.Address, PrivateAddress: host.PrivateAddress,
			Reachability: servicecatalog.ReachabilityName(host.Reachability), Enabled: host.Status == servicecatalog.Enabled})
	}
	for _, deployment := range update.Deployments {
		rsp.Deployments = append(rsp.Deployments, &directorypb.DirectoryDeployment{HostId: deployment.HostID, ComponentId: deployment.ComponentID, Enabled: deployment.Status == servicecatalog.Enabled})
	}
	return rsp
}

func serveDirectory(t *testing.T, handler directoryServiceFunc) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	directorypb.RegisterDirectoryService(svc, handler)
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	return listener.Addr().String()
}

func TestLocalDirectorySourceUsesRealLoopbackTRPC(t *testing.T) {
	directory := versioned(t, DirectoryUpdate{
		Hosts: []servicecatalog.Host{
			{ID: "storage", Address: "storage.example.test", PrivateAddress: "10.0.0.2", Status: servicecatalog.Enabled},
			{ID: "home", Address: "192.168.0.103", Status: servicecatalog.Enabled, Reachability: servicecatalog.ReachabilityOutbound},
			{ID: "retired", Address: "retired.example.test", Status: servicecatalog.Disabled},
		},
		Deployments: []servicecatalog.Deployment{
			{HostID: "storage", ComponentID: "admin", Status: servicecatalog.Enabled},
			{HostID: "home", ComponentID: "monitor", Status: servicecatalog.Disabled},
			{HostID: "retired", ComponentID: "monitor", Status: servicecatalog.Enabled},
		},
	})
	address := serveDirectory(t, func(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
		if req.GetCurrentVersion() == directory.Version {
			return &directorypb.GetDirectoryRsp{Version: directory.Version}, nil
		}
		return directoryResponse(directory), nil
	})
	source, err := NewLocalDirectorySource(address, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	update, err := source.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.True(t, update.Changed)
	update.Changed = false
	require.Equal(t, directory, update, "主机的可达性、启停与部署的启停原样带回")
	// 客户端按内嵌目录展开：只算启用主机上启用的部署，出站主机由控制机（部署了 admin 的那台）中转。
	expanded := resolved(t, update)
	require.Equal(t, []string{"storage"}, expanded.Services[secretService])
	require.NotContains(t, expanded.Services, monitorService)
	require.Equal(t, map[string]servicecatalog.DirectoryHost{
		"storage": {Address: "storage.example.test", PrivateAddress: "10.0.0.2"},
		"home":    {Address: "192.168.0.103", Relay: "storage"},
	}, expanded.Hosts)
	update, err = source.Fetch(context.Background(), directory.Version)
	require.NoError(t, err)
	require.False(t, update.Changed)
	require.Equal(t, directory.Version, update.Version)
	_, err = NewLocalDirectorySource("example.test:11002", time.Second)
	require.Error(t, err)
	_, err = NewLocalDirectorySource("192.0.2.1:11002", time.Second)
	require.Error(t, err)
}

// 不认识的可达性取值整份拒绝，不当成可直达。
func TestLocalDirectorySourceRejectsUnknownReachability(t *testing.T) {
	address := serveDirectory(t, func(context.Context, *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
		return &directorypb.GetDirectoryRsp{Changed: true, Version: "v1", Hosts: []*directorypb.DirectoryHost{{HostId: "a", Address: "a.example.test", Reachability: "satellite", Enabled: true}}}, nil
	})
	source, err := NewLocalDirectorySource(address, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	_, err = source.Fetch(context.Background(), "")
	require.ErrorContains(t, err, "unknown reachability")
}
