package router

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
)

type staticNativeTable struct{ route gatewayproxy.Route }

func (t staticNativeTable) ResolveRPC(rpcName string) (gatewayproxy.Route, string, bool) {
	_, method, ok := splitRPCName(rpcName)
	return t.route, method, ok
}

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())
	return addr
}

func startWildcardServer(t *testing.T, addr, name string, handle func(context.Context, *codec.Body) (*codec.Body, error)) {
	t.Helper()
	svc := server.New(server.WithAddress(addr), server.WithNetwork("tcp"), server.WithProtocol("trpc"),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName(name))
	desc := &server.ServiceDesc{ServiceName: name, HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
		req := &codec.Body{}
		filters, err := f(req)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, req, func(ctx context.Context, body interface{}) (interface{}, error) {
			return handle(ctx, body.(*codec.Body))
		})
	}}}}
	require.NoError(t, svc.Register(desc, struct{}{}))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	time.Sleep(200 * time.Millisecond)
}

// An upstream slower than the route timeout must be answered with an error
// right away, not dropped: tRPC suppresses the reply for full-link timeouts and
// the caller would otherwise hang until its own deadline.
func TestNativeUpstreamTimeoutAnswersCallerPromptly(t *testing.T) {
	upstream, gateway := freeAddr(t), freeAddr(t)
	startWildcardServer(t, upstream, "trpc.moox.storage.DataView", func(ctx context.Context, req *codec.Body) (*codec.Body, error) {
		time.Sleep(3 * time.Second)
		return &codec.Body{Data: []byte(`{"ok":true}`)}, nil
	})
	creds := gatewayauth.Credentials{KeyID: "moox-gateway-service", Secret: "secret"}
	desc, impl := NativeServiceDesc(NativeOptions{NodeID: "storage", Credentials: creds, Table: staticNativeTable{route: gatewayproxy.Route{
		Address: upstream, ServicePath: "trpc.moox.storage.DataView", TimeoutMS: 1000, AllowedMethods: []string{"*"},
	}}})
	svc := server.New(server.WithAddress(gateway), server.WithNetwork("tcp"), server.WithProtocol("trpc"),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithServiceName("trpc.moox.gateway.ServiceGateway"))
	require.NoError(t, svc.Register(desc, impl))
	go func() { _ = svc.Serve() }()
	t.Cleanup(func() { _ = svc.Close(nil) })
	time.Sleep(200 * time.Millisecond)

	path := "/trpc.moox.storage.DataView/QueryTimeSeriesRows"
	body := []byte(`{"view_id":"v"}`)
	signed, err := gatewayauth.Sign(creds, gatewayauth.Request{Caller: "admin-gateway", Method: http.MethodPost, Path: path, TargetNode: "storage", Callee: "trpc.moox.storage.DataView", Func: "QueryTimeSeriesRows", Body: body}, time.Now())
	require.NoError(t, err)
	opts := []client.Option{client.WithTarget("ip://" + gateway), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithServiceName("trpc.moox.storage.DataView"), client.WithCalleeMethod("QueryTimeSeriesRows"), client.WithSerializationType(codec.SerializationTypeNoop), client.WithCurrentSerializationType(codec.SerializationTypeNoop), client.WithTimeout(20 * time.Second)}
	for name, values := range signed {
		opts = append(opts, client.WithMetaData(name, []byte(values[0])))
	}
	opts = append(opts, client.WithSerializationType(codec.SerializationTypeJSON))
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithClientRPCName(path)
	start := time.Now()
	err = client.New().Invoke(ctx, &codec.Body{Data: body}, &codec.Body{}, opts...)
	waited := time.Since(start)

	require.Error(t, err)
	require.Less(t, waited, 2500*time.Millisecond, "the caller must not wait for its own 20s deadline")
	require.Equal(t, errs.RetClientTimeout, errs.Code(err))
	require.Contains(t, errs.Msg(err), "native gateway upstream trpc.moox.storage.DataView/QueryTimeSeriesRows timed out")
}
