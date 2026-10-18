package listener_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/listener"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testrpc"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
)

// 停止时已接纳的长请求必须完成并返回响应，而不是被直接断开。
func TestTRPCDrainsInFlightRequestOnClose(t *testing.T) {
	opened, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	gate := &healthz.DrainGate{}
	svc := listener.TRPC(opened, "trpc.test.Drain", gate)
	desc := &server.ServiceDesc{ServiceName: "trpc.test.Drain", HandlerType: ((*interface{})(nil)), Methods: []server.Method{{Name: "*", Func: func(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
		raw := &codec.Body{}
		filters, err := f(raw)
		if err != nil {
			return nil, err
		}
		return filters.Filter(ctx, raw, func(context.Context, interface{}) (interface{}, error) {
			close(entered)
			<-release
			return &codec.Body{Data: []byte("done")}, nil
		})
	}}}}
	require.NoError(t, svc.Register(desc, struct{}{}))
	served := make(chan error, 1)
	go func() { served <- svc.Serve() }()

	type result struct {
		body []byte
		err  error
	}
	called := make(chan result, 1)
	go func() {
		body, err := testrpc.Call(context.Background(), opened.Addr().String(), nil, "trpc.test.Drain", "Slow", codec.SerializationTypePB, []byte("x"), nil)
		called <- result{body, err}
	}()
	<-entered

	gate.Begin()
	closed := make(chan struct{})
	go func() { _ = svc.Close(nil); close(closed) }()
	// 排空期间新到达的请求必须被拒绝，不能进入业务处理。
	_, rejectedErr := testrpc.Call(context.Background(), opened.Addr().String(), nil, "trpc.test.Drain", "Late", codec.SerializationTypePB, []byte("y"), nil)
	require.Error(t, rejectedErr)
	require.Equal(t, errs.RetServerNoService, errs.Code(rejectedErr))
	select {
	case <-closed:
		t.Fatal("Close 在途请求完成前就返回了")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	got := <-called
	require.NoError(t, got.err)
	require.Equal(t, []byte("done"), got.body)
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("在途请求完成后 Close 没有返回")
	}
	<-served
}
