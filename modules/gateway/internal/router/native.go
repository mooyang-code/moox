package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"github.com/mooyang-code/moox/packages/trpcretry"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
)

type nativeRouteTable interface {
	ResolveRPC(string) (gatewayproxy.Route, string, bool)
}

type NativeOptions struct {
	NodeID             string
	Credentials        gatewayauth.Credentials
	CredentialRegistry credentialVerifier
	Table              nativeRouteTable
	Nonces             nonceConsumer
	Disabled           func() bool
	Now                func() time.Time
	upstreamOptions    []client.Option
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
			return nil, fmt.Errorf("native gateway route not found: %s", rpcName)
		}
		if route.MaxBodyBytes > 0 && int64(len(req.Data)) > route.MaxBodyBytes {
			return nil, errors.New("native gateway request body exceeds route limit")
		}
		metadata := codec.Message(ctx).ServerMetaData()
		headers := make(http.Header, len(metadata))
		for key, value := range metadata {
			headers.Add(key, string(value))
		}
		claims, err := proxy.verify(gatewayauth.Request{
			Method: "POST", Path: "/" + strings.TrimPrefix(rpcName, "/"), TargetNode: proxy.options.NodeID, Callee: route.ServicePath, Func: method, Body: req.Data,
		}, headers)
		if err != nil {
			return nil, err
		}
		if !nativeCallerPolicyAllows(claims.Caller, route.ServicePath, method) {
			return nil, errors.New("native gateway caller is not allowed for route")
		}
		if len(route.AllowedCallers) > 0 && !route.AllowsCaller(claims.Caller) {
			return nil, errors.New("native gateway caller is not allowed for route")
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
		serializationType := codec.Message(ctx).SerializationType()
		invokeOptions := []client.Option{
			client.WithTarget("ip://" + route.Address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
			client.WithServiceName(route.ServicePath), client.WithCalleeMethod(method),
			client.WithSerializationType(serializationType), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
			client.WithTimeout(time.Duration(route.TimeoutMS) * time.Millisecond),
		}
		if nativeReadOnlyMethod(method) {
			invokeOptions = append(invokeOptions, client.WithFilter(trpcretry.ReadOnly()))
		}
		codec.Message(upstreamCtx).WithClientRPCName("/" + route.ServicePath + "/" + method)
		for key, value := range metadata {
			if strings.HasPrefix(strings.ToLower(key), "x-moox-") {
				continue
			}
			invokeOptions = append(invokeOptions, client.WithMetaData(key, value))
		}
		invokeOptions = append(invokeOptions, proxy.options.upstreamOptions...)
		if err := client.New().Invoke(upstreamCtx, req, response,
			invokeOptions...,
		); err != nil {
			return nil, err
		}
		if route.MaxBodyBytes > 0 && int64(len(response.Data)) > route.MaxBodyBytes {
			return nil, errors.New("native gateway response body exceeds route limit")
		}
		return response, nil
	})
}

// nativeReadOnlyMethod keeps retries limited to idempotent reads. Gateway
// routes also carry mutating RPCs, so relying on method-name prefixes alone
// would make an accidental write retry possible.
func nativeReadOnlyMethod(method string) bool {
	switch method {
	case "GetSpace", "ListSpaces", "GetDataSource", "ListDataSources", "GetSubject", "ListSubjects", "ListSubjectSymbols",
		"GetDataset", "ListDatasets", "ListDatasetSubjects", "GetFieldGroup", "ListFieldGroups",
		"GetField", "ListFields", "GetFactor", "ListFactors", "ListDatasetColumns", "GetView",
		"ListViews", "ListViewColumns", "GetDataNode", "ListDataNodes", "CheckDatasetActivation",
		"ListArchiveFiles", "ReadFields", "ReadTimeSeriesRows", "ReadRecordRows", "QueryTimeSeriesRows",
		"SearchRecordRows":
		return true
	default:
		return false
	}
}

func (proxy *nativeProxy) verify(request gatewayauth.Request, header http.Header) (gatewayauth.Claims, error) {
	if proxy.options.CredentialRegistry != nil {
		return proxy.options.CredentialRegistry.Verify(request, header, proxy.options.Now())
	}
	return gatewayauth.Verify(proxy.options.Credentials, request, header, proxy.options.Now())
}
