package gatewayclient

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

// 跨主机调用默认压缩：远端网关、经 control 中转、SSH 隧道、Access 都压缩；本机回环从不压缩；显式 none 全部关闭。
func TestCrossHostEndpointsCompressByDefault(t *testing.T) {
	ca, _, _ := testCertificates(t)
	directory := testDirectory(t, "a", "b")
	for _, compression := range []string{"", CompressionGzip, CompressionNone} {
		want := compression != CompressionNone
		t.Run("compression="+compression, func(t *testing.T) {
			// Tunnel：每个目标主机经 SSH 本地转发。
			tunnel := tunnelConfig(&sourceStub{directory: directory})
			tunnel.Compression = compression
			c := newTestClient(t, tunnel)
			ep, err := c.endpoint(context.Background(), secretService, "")
			require.NoError(t, err)
			require.Equal(t, want, ep.compress, "SSH 隧道经公网到达目标主机")

			// External：经 Access。
			external := newTestClient(t, Config{Mode: External, Credentials: gatewayauth.Credentials{Caller: "scf-collector", KeyID: "k", Secret: "fixture-key"},
				AccessAddress: "access.example.test:11004", AccessInstanceID: "access@storage", Compression: compression})
			ep, err = external.endpoint(context.Background(), secretService, "")
			require.NoError(t, err)
			require.Equal(t, want, ep.compress, "经 Access 的调用")

			// Internal：本机回环不压缩，远端主机与中转按配置压缩。
			internal := &Client{config: Config{Mode: Internal, LocalHostID: "a", LocalAddress: "127.0.0.1:11002", CAFile: ca, Compression: compression}}
			internal.directory = directory
			ep, err = internal.endpoint(context.Background(), secretService, "")
			require.NoError(t, err)
			require.Equal(t, "a", ep.target)
			require.False(t, ep.compress, "本机回环从不压缩")
			ep, err = internal.endpoint(context.Background(), secretService, "a")
			require.NoError(t, err)
			require.Equal(t, "b", ep.target)
			require.Equal(t, want, ep.compress, "远端主机网关")

			relayed := directory.Clone()
			relayed.Hosts["b"] = servicecatalog.DirectoryHost{Address: "b.example.test", Relay: "a"}
			internal.directory = relayed
			ep, err = internal.endpoint(context.Background(), secretService, "a")
			require.NoError(t, err)
			require.NotNil(t, ep.relay)
			require.Equal(t, want, ep.compress, "经 control 中转")
		})
	}
}

// 读接口的请求很小、响应很大：服务端按请求的压缩方式压缩响应，所以小请求也必须标记压缩，响应才会被压缩。
func TestTinyRequestStillGetsACompressedResponse(t *testing.T) {
	response := []byte(`{"rows":"` + strings.Repeat("kline-row-0123456789,", 4000) + `"}`)
	run := func(compress bool) int64 {
		base, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		listener := &byteCountingListener{Listener: base}
		svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(base.Addr().String()),
			server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName("trpc.moox.test.Reader"))
		desc := &server.ServiceDesc{ServiceName: "trpc.moox.test.Reader", HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
			request := &codec.Body{}
			filters, err := f(request)
			if err != nil {
				return nil, err
			}
			return filters.Filter(ctx, request, func(context.Context, interface{}) (interface{}, error) { return &codec.Body{Data: response}, nil })
		}}}}
		require.NoError(t, svc.Register(desc, struct{}{}))
		go func() { _ = svc.Serve() }()
		t.Cleanup(func() { _ = svc.Close(nil) })

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pool := newRPCPool()
		defer pool.Close()
		rsp, err := pool.invoke(ctx, endpoint{address: base.Addr().String(), target: "storage", compress: compress}, secretService, "GetSecret", codec.SerializationTypeJSON, []byte(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, response, rsp)
		return listener.written.Load()
	}
	plain := run(false)
	compressed := run(true)
	require.Less(t, compressed*5, plain, "两字节的请求也要标记压缩，大响应才会被压缩")
}
