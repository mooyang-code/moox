package router

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/rpcconn"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/transport"
)

type Upstream struct {
	pool      *rpcconn.Pool
	transport transport.ClientTransport
}

func NewUpstream() *Upstream {
	return &Upstream{pool: rpcconn.New(nil), transport: transport.NewClientTransport()}
}
func (u *Upstream) Close() error { return u.pool.Close() }

func (u *Upstream) Forward(parent context.Context, route servicecatalog.Route, serialization int, body []byte, metadata codec.MetaData) ([]byte, error) {
	ctx, message := codec.WithNewMessage(parent)
	defer codec.PutBackMessage(message)
	message.WithClientRPCName("/" + route.ServicePath + "/" + route.Method)
	options := []client.Option{
		client.WithTarget("ip://" + route.Address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(route.ServicePath), client.WithCalleeMethod(route.Method),
		client.WithSerializationType(serialization), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithTransport(u.transport), client.WithPool(u.pool), client.WithMultiplexed(false),
		client.WithTimeout(time.Duration(route.TimeoutMS) * time.Millisecond), client.WithDialTimeout(time.Duration(route.TimeoutMS) * time.Millisecond),
	}
	// 网关只转发一次：只读调用的重发由调用方的 gatewayclient 负责，这里再重试会让一次失败放大成多次上游请求。
	// metadata 已由路由层筛过：外面带进来的 x-moox- 头不在里面。
	for key, value := range metadata {
		options = append(options, client.WithMetaData(key, append([]byte(nil), value...)))
	}
	rsp := &codec.Body{}
	if err := client.DefaultClient.Invoke(ctx, &codec.Body{Data: body}, rsp, options...); err != nil {
		return nil, err
	}
	return rsp.Data, nil
}

func failureKind(err error) string {
	var frame *errs.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &frame) && frame.IsTimeout(errs.ErrorTypeFramework) {
		return "timeout"
	}
	return "connection"
}

// tRPC suppresses full-link timeout replies. Translate the upstream deadline
// so callers receive the route timeout instead of waiting for their own limit.
func routeError(route servicecatalog.Route, err error) error {
	if failureKind(err) == "timeout" {
		return errs.NewFrameError(errs.RetClientTimeout, fmt.Sprintf("host gateway upstream %s/%s timed out after %dms", route.ServicePath, route.Method, route.TimeoutMS))
	}
	return err
}
