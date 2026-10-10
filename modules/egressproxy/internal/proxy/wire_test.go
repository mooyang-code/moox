package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestNativePBAndJSONWire(t *testing.T) {
	p, _ := tlsFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		if r.URL.Path == "/large" {
			_, _ = io.CopyN(w, zeroReader{}, MaxResponseBytes)
			return
		}
		_, _ = io.WriteString(w, `{"retry":true}`)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.egress.Proxy"))
	require.NoError(t, svc.Register(&egresspb.ProxyServer_ServiceDesc, p))
	done := make(chan error, 1)
	go func() { done <- svc.Serve() }()
	t.Cleanup(func() {
		require.NoError(t, svc.Close(nil))
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("native egress service did not stop")
		}
	})
	for _, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		proxyClient := egresspb.NewProxyClientProxy(client.WithTarget("ip://"+listener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithSerializationType(serialization), client.WithTimeout(5*time.Second))
		rsp, err := proxyClient.Do(context.Background(), request())
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		require.Equal(t, int32(429), rsp.Status)
		require.Equal(t, `{"retry":true}`, string(rsp.Body))
		dns, err := proxyClient.ResolveDomains(context.Background(), &egresspb.ResolveDomainsReq{Domains: []string{"api.binance.com"}})
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_INNER_ERR, dns.GetRetInfo().GetCode())
		denied := request()
		denied.Host = "evil.test"
		rsp, err = proxyClient.Do(context.Background(), denied)
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_NO_PERMISSION, rsp.GetRetInfo().GetCode())
	}
	for _, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		proxyClient := egresspb.NewProxyClientProxy(client.WithTarget("ip://"+listener.Addr().String()), client.WithProtocol("trpc"), client.WithSerializationType(serialization), client.WithTimeout(10*time.Second))
		large := request()
		large.Path, large.TimeoutMs = "/large", 10000
		rsp, err := proxyClient.Do(context.Background(), large)
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), strings.TrimSpace(rsp.GetRetInfo().GetMsg()))
		require.Len(t, rsp.Body, MaxResponseBytes)
	}
}
