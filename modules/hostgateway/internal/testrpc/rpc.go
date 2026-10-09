// Package testrpc starts real go-net tRPC fixtures with raw PB/JSON payloads.
package testrpc

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/listener"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/rpcconn"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func Serve(t testing.TB, opened net.Listener, name string, handler func(context.Context, []byte) ([]byte, error)) {
	t.Helper()
	svc := listener.TRPC(opened, name)
	desc := &server.ServiceDesc{ServiceName: name, HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
		raw := &codec.Body{}
		filters, err := f(raw)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, raw, func(ctx context.Context, body interface{}) (interface{}, error) {
			rsp, err := handler(ctx, body.(*codec.Body).Data)
			return &codec.Body{Data: rsp}, err
		})
	}}}}
	if err := svc.Register(desc, struct{}{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.Serve() }()
	t.Cleanup(func() {
		_ = svc.Close(nil)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("fixture server did not stop")
		}
	})
}

func Signed(credentials gatewayauth.Credentials, target, service, method string, body []byte) (http.Header, error) {
	return gatewayauth.Sign(credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: target, Callee: service, Func: method, Body: body}, time.Now())
}

func Call(parent context.Context, address string, trust *tls.Config, service, method string, serialization int, body []byte, headers http.Header) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	ctx, message := codec.WithNewMessage(ctx)
	defer codec.PutBackMessage(message)
	message.WithClientRPCName("/" + service + "/" + method)
	pool := rpcconn.New(trust)
	defer pool.Close()
	opts := []client.Option{client.WithTarget("ip://" + address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(service), client.WithCalleeMethod(method), client.WithSerializationType(serialization), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithTransport(transport.NewClientTransport()), client.WithPool(pool), client.WithMultiplexed(false), client.WithTimeout(8 * time.Second), client.WithDialTimeout(2 * time.Second)}
	for key, values := range headers {
		if len(values) != 1 {
			return nil, errors.New("fixture headers must have one value")
		}
		opts = append(opts, client.WithMetaData(key, []byte(values[0])))
	}
	rsp := &codec.Body{}
	if err := client.DefaultClient.Invoke(ctx, &codec.Body{Data: body}, rsp, opts...); err != nil {
		return nil, err
	}
	return rsp.Data, nil
}

func Address(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
