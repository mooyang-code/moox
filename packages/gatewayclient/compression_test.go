package gatewayclient

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/wirecompress"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

// 跨主机调用恒定压缩：远端网关、经 control 中转、SSH 隧道、Access 都压缩；本机回环从不压缩。
func TestCrossHostEndpointsAlwaysCompress(t *testing.T) {
	ca, _, _ := testCertificates(t)
	directory := testDirectory(t, "a", "b")

	// Tunnel：每个目标主机经 SSH 本地转发。
	c := newTestClient(t, tunnelConfig(&sourceStub{directory: directory}))
	ep, err := c.endpoint(context.Background(), secretService, "")
	require.NoError(t, err)
	require.True(t, ep.compress, "SSH 隧道经公网到达目标主机")

	// External：经 Access。
	external := newTestClient(t, Config{Mode: External, Credentials: gatewayauth.Credentials{Caller: "scf-collector", KeyID: "k", Secret: "fixture-key"},
		AccessAddress: "access.example.test:11004", AccessInstanceID: "access@storage"})
	ep, err = external.endpoint(context.Background(), secretService, "")
	require.NoError(t, err)
	require.True(t, ep.compress, "经 Access 的调用")

	// Internal：本机回环不压缩，远端主机与中转压缩。
	internal := &Client{config: Config{Mode: Internal, LocalHostID: "a", LocalAddress: "127.0.0.1:11002", CAFile: ca}}
	internal.directory = directory
	ep, err = internal.endpoint(context.Background(), secretService, "")
	require.NoError(t, err)
	require.Equal(t, "a", ep.target)
	require.False(t, ep.compress, "本机回环从不压缩")
	ep, err = internal.endpoint(context.Background(), secretService, "a")
	require.NoError(t, err)
	require.Equal(t, "b", ep.target)
	require.True(t, ep.compress, "远端主机网关")

	relayed := directory.Clone()
	relayed.Hosts["b"] = servicecatalog.DirectoryHost{Address: "b.example.test", Relay: "a"}
	internal.directory = relayed
	ep, err = internal.endpoint(context.Background(), secretService, "a")
	require.NoError(t, err)
	require.NotNil(t, ep.relay)
	require.True(t, ep.compress, "经 control 中转")
}

type spacedRequest struct{ Space string }

func (r spacedRequest) GetSpaceId() string { return r.Space }

// 压缩类型的选择：沿用入站类型 > context 里的空间 > 默认字典；Invoke 会把请求顶层的 space_id 写进 context。
func TestCompressTypeSelection(t *testing.T) {
	defaultType := wirecompress.CompressType("")
	require.Equal(t, defaultType, compressTypeFor(context.Background()), "没有空间用默认字典")
	require.Equal(t, defaultType, compressTypeFor(wirecompress.WithSpace(context.Background(), "no-dedicated-dict")), "没有专用字典的空间用默认字典")
	require.Equal(t, defaultType, compressTypeFor(WithCompressType(context.Background(), 0)), "未压缩类型不能沿用")
	require.Equal(t, defaultType, compressTypeFor(WithCompressType(context.Background(), codec.CompressTypeGzip)), "非本包注册的类型不能沿用")
	require.Equal(t, defaultType, compressTypeFor(WithCompressType(context.Background(), 200)), "未注册的类型号不能沿用")
	require.Equal(t, defaultType, compressTypeFor(WithCompressType(context.Background(), defaultType)))
}

func TestInvokePutsRequestSpaceIntoContext(t *testing.T) {
	config := tunnelConfig(&sourceStub{directory: testDirectory(t, "a")})
	config.Serialization = codec.SerializationTypeJSON
	c := newTestClient(t, config)
	var seen string
	c.invoke = func(ctx context.Context, _ endpoint, _, _ string, _ int, _ []byte, _ http.Header) ([]byte, error) {
		seen = wirecompress.SpaceOf(ctx, nil)
		return []byte(`{}`), nil
	}
	rsp := map[string]any{}
	require.NoError(t, c.Invoke(context.Background(), secretService, "GetSecret", map[string]any{}, &rsp))
	require.Equal(t, "", seen, "请求不带空间字段时不设置")
	c.invoke = func(ctx context.Context, _ endpoint, _, _ string, _ int, _ []byte, _ http.Header) ([]byte, error) {
		seen = wirecompress.SpaceOf(ctx, nil)
		return []byte(`{}`), nil
	}
	require.NoError(t, c.Invoke(context.Background(), secretService, "GetSecret", &spaceCarrier{Space: "ashare"}, &rsp))
	require.Equal(t, "ashare", seen, "请求顶层的 space_id 决定字典")
	require.NoError(t, c.Invoke(wirecompress.WithSpace(context.Background(), "crypto"), secretService, "GetSecret", &spaceCarrier{Space: "ashare"}, &rsp))
	require.Equal(t, "crypto", seen, "调用点显式指定的空间优先")
}

// spaceCarrier 是可 JSON 序列化的带空间请求。
type spaceCarrier struct {
	Space string `json:"space_id"`
}

func (r *spaceCarrier) GetSpaceId() string { return r.Space }

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
		for _, request := range [][]byte{nil, {}, []byte(`{}`)} {
			rsp, err := pool.invoke(ctx, endpoint{address: base.Addr().String(), target: "storage", compress: compress}, secretService, "GetSecret", codec.SerializationTypeJSON, request, nil)
			require.NoError(t, err, "空请求体与极小请求体在压缩时也要能完整往返")
			require.Equal(t, response, rsp)
		}
		return listener.written.Load()
	}
	plain := run(false)
	compressed := run(true)
	require.Less(t, compressed*5, plain, "空请求和两字节的请求也要标记压缩，大响应才会被压缩")
}
