package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
)

type nativeRouteTable interface {
	ResolveRPC(string) (gatewayproxy.Route, string, bool)
}

type NativeOptions struct {
	NodeID      string
	Credentials gatewayauth.Credentials
	Table       nativeRouteTable
	Nonces      nonceConsumer
	Disabled    func() bool
	Now         func() time.Time
}

// NativeServiceDesc is a wildcard tRPC service descriptor. Route snapshots
// can change without restarting the gateway.
func NativeServiceDesc(options NativeOptions) (*server.ServiceDesc, interface{}) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Disabled == nil {
		options.Disabled = func() bool { return false }
	}
	proxy := &nativeProxy{options: options}
	return &server.ServiceDesc{
		ServiceName: "trpc.moox.gateway.ServiceGateway",
		HandlerType: ((*interface{})(nil)),
		Methods:     []server.Method{{Name: "*", Func: proxy.handle}},
	}, proxy
}

type nativeProxy struct{ options NativeOptions }

func (proxy *nativeProxy) handle(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
	request := &codec.Body{}
	filters, err := f(request)
	if err != nil {
		return nil, err
	}
	return filters.Filter(ctx, request, func(ctx context.Context, body interface{}) (interface{}, error) {
		req, ok := body.(*codec.Body)
		if !ok || req == nil {
			return nil, errors.New("native gateway request body is invalid")
		}
		if proxy.options.Disabled() {
			return nil, errors.New("native gateway is disabled")
		}
		rpcName := codec.Message(ctx).ServerRPCName()
		route, method, ok := proxy.options.Table.ResolveRPC(rpcName)
		if !ok {
			return nil, errors.New("native gateway route not found")
		}
		if route.MaxBodyBytes > 0 && int64(len(req.Data)) > route.MaxBodyBytes {
			return nil, errors.New("native gateway request body exceeds route limit")
		}
		metadata := codec.Message(ctx).ServerMetaData()
		headers := make(http.Header, len(metadata))
		for key, value := range metadata {
			headers.Add(key, string(value))
		}
		claims, err := gatewayauth.Verify(proxy.options.Credentials, gatewayauth.Request{
			Method: "POST", Path: "/" + strings.TrimPrefix(rpcName, "/"), TargetNode: proxy.options.NodeID, Body: req.Data,
		}, headers, proxy.options.Now())
		if err != nil {
			return nil, err
		}
		if proxy.options.Nonces != nil {
			consumed, err := proxy.options.Nonces.Consume(ctx, serviceNonceNamespace, claims.Nonce, claims.TTL)
			if err != nil {
				return nil, err
			}
			if !consumed {
				return nil, errors.New("native gateway request replayed")
			}
		}
		response := &codec.Body{}
		upstreamCtx, cancel := context.WithTimeout(ctx, time.Duration(route.TimeoutMS)*time.Millisecond)
		defer cancel()
		invokeOptions := []client.Option{
			client.WithTarget("ip://" + route.Address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
			client.WithServiceName(route.ServicePath), client.WithCalleeMethod(method),
			client.WithSerializationType(codec.SerializationTypeNoop), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
			client.WithTimeout(time.Duration(route.TimeoutMS) * time.Millisecond),
		}
		for key, value := range metadata {
			invokeOptions = append(invokeOptions, client.WithMetaData(key, value))
		}
		if err := client.New().Invoke(upstreamCtx, req, response,
			invokeOptions...,
		); err != nil {
			return nil, err
		}
		return response, nil
	})
}
