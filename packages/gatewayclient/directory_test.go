package gatewayclient

import (
	"context"
	"net"
	"testing"
	"time"

	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type directoryServiceFunc func(context.Context, *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error)

func (f directoryServiceFunc) GetDirectory(ctx context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
	return f(ctx, req)
}

func TestLocalDirectorySourceUsesRealLoopbackTRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	directory := testDirectory(t, "storage")
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"))
	directorypb.RegisterDirectoryService(svc, directoryServiceFunc(func(_ context.Context, req *directorypb.GetDirectoryReq) (*directorypb.GetDirectoryRsp, error) {
		if req.GetCurrentVersion() == directory.Version {
			return &directorypb.GetDirectoryRsp{Version: directory.Version}, nil
		}
		return &directorypb.GetDirectoryRsp{Changed: true, Version: directory.Version,
			Services: map[string]*directorypb.ServiceHosts{secretService: {HostIds: []string{"storage"}}},
			Hosts:    map[string]*directorypb.DirectoryHost{"storage": {Address: "storage.example.test"}}}, nil
	}))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	source, err := NewLocalDirectorySource(listener.Addr().String(), time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	update, err := source.Fetch(context.Background(), "")
	require.NoError(t, err)
	require.True(t, update.Changed)
	require.Equal(t, directory, update.Directory)
	require.NoError(t, update.Directory.Validate())
	update, err = source.Fetch(context.Background(), directory.Version)
	require.NoError(t, err)
	require.False(t, update.Changed)
	require.Equal(t, directory.Version, update.Directory.Version)
	_, err = NewLocalDirectorySource("example.test:11002", time.Second)
	require.Error(t, err)
	_, err = NewLocalDirectorySource("192.0.2.1:11002", time.Second)
	require.Error(t, err)
}
