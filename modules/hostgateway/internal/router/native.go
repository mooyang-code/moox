// Package router 是主机网关唯一的转发入口：校验服务签名，按 service path 和方法查快照中的路由与 ACL，
// 再把请求原样转发到本机服务（127.0.0.1:<端口>）。
package router

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/trpcretry"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
)

// ServiceName 是主机网关两个入口（11003、11002）使用的 tRPC 服务名。
const ServiceName = "trpc.moox.hostgateway.Gateway"

const serviceNonceNamespace = "host-gateway"

type nonceConsumer interface {
	Consume(context.Context, string, string, time.Duration) (bool, error)
}

// Source 提供当前快照。
type Source interface {
	Load() *snapshot.Applied
}

// Metrics 记录转发结果。
type Metrics interface {
	AuthFailed()
	ReplayFailed()
	UpstreamFailed(string)
	ObserveRequest(string, string, int, time.Duration)
}

// Options 是转发入口的依赖。
type Options struct {
	HostID          string
	Snapshot        Source
	Nonces          nonceConsumer
	Metrics         Metrics
	Catalog         *servicecatalog.Catalog
	Now             func() time.Time
	upstreamOptions []client.Option
}

// ServiceDesc 返回通配方法的服务描述：路由来自快照，变化时不需要重启网关。
func ServiceDesc(options Options) (*server.ServiceDesc, interface{}) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Catalog == nil {
		options.Catalog = servicecatalog.Default()
	}
	proxy := &nativeProxy{options: options}
	return &server.ServiceDesc{
		ServiceName: ServiceName,
		HandlerType: ((*interface{})(nil)),
		Methods:     []server.Method{{Name: "*", Func: proxy.handle}},
	}, proxy
}

type nativeProxy struct{ options Options }

func (proxy *nativeProxy) handle(_ interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
	request := &codec.Body{}
	filters, err := f(request)
	if err != nil {
		return nil, err
	}
	return filters.Filter(ctx, request, func(ctx context.Context, body interface{}) (interface{}, error) {
		req, ok := body.(*codec.Body)
		if !ok || req == nil {
			return nil, errors.New("主机网关请求体无效")
		}
		started := time.Now()
		service, method, status := "unauthenticated", "unauthenticated", 0
		defer func() {
			if proxy.options.Metrics != nil {
				proxy.options.Metrics.ObserveRequest(service, method, status, time.Since(started))
			}
		}()
		rsp, routedService, routedMethod, err := proxy.forward(ctx, req)
		if routedService != "" {
			service, method = routedService, routedMethod
		}
		if err != nil {
			status = int(errs.Code(err))
			return nil, err
		}
		return rsp, nil
	})
}

func (proxy *nativeProxy) forward(ctx context.Context, req *codec.Body) (*codec.Body, string, string, error) {
	applied := proxy.options.Snapshot.Load()
	if applied == nil {
		return nil, "", "", errs.New(gatewayroute.RetServiceNotHere, "主机网关还没有拿到快照")
	}
	if applied.Disabled {
		return nil, "", "", errs.New(gatewayroute.RetHostDisabled, fmt.Sprintf("主机 %s 已停用", proxy.options.HostID))
	}
	msg := codec.Message(ctx)
	rpcName := msg.ServerRPCName()
	servicePath, method, ok := splitRPCName(rpcName)
	if !ok {
		return nil, "", "", errs.New(gatewayroute.RetServiceNotHere, fmt.Sprintf("无效的 RPC 名 %q", rpcName))
	}
	metadata := msg.ServerMetaData()
	headers := make(http.Header, len(metadata))
	for key, value := range metadata {
		headers.Add(key, string(value))
	}
	if applied.Registry == nil {
		proxy.authFailed()
		return nil, "", "", errs.New(gatewayroute.RetUnauthenticated, "主机网关没有可用的校验密钥")
	}
	claims, err := applied.Registry.Verify(gatewayauth.Request{
		Method: http.MethodPost, Path: "/" + servicePath + "/" + method, TargetNode: proxy.options.HostID,
		Callee: servicePath, Func: method, Body: req.Data,
	}, headers, proxy.options.Now())
	if err != nil {
		proxy.authFailed()
		// 失败的具体原因（未知 KeyID、时间窗、签名不符）只写日志，不返回给调用方。
		log.Printf("主机网关签名校验失败: %v", err)
		return nil, "", "", errs.New(gatewayroute.RetUnauthenticated, "服务签名校验失败")
	}
	route, resolvedMethod, ok := applied.Table.ResolveRPCForCaller(rpcName, claims.Caller)
	if !ok {
		// 被拒绝的请求不用请求里的服务名和方法名作指标标签：已验签的调用方也可以请求任意的名字，
		// 直接作标签会让序列数无限增长。
		if _, _, served := applied.Table.ResolveRPC(rpcName); served {
			return nil, rejectedMetricLabel, rejectedMetricLabel, errs.New(gatewayroute.RetForbidden, fmt.Sprintf("调用方 %s 不能调用 %s/%s", claims.Caller, servicePath, method))
		}
		if applied.Table.HasService(servicePath) {
			return nil, rejectedMetricLabel, rejectedMetricLabel, errs.New(gatewayroute.RetForbidden, fmt.Sprintf("方法 %s/%s 不经主机网关开放", servicePath, method))
		}
		return nil, rejectedMetricLabel, rejectedMetricLabel, errs.New(gatewayroute.RetServiceNotHere, fmt.Sprintf("服务 %s 不在主机 %s 上", servicePath, proxy.options.HostID))
	}
	method = resolvedMethod
	if route.MaxBodyBytes > 0 && int64(len(req.Data)) > route.MaxBodyBytes {
		return nil, servicePath, method, errs.New(gatewayroute.RetBodyTooLarge, "请求体超过路由的包体上限")
	}
	if proxy.options.Nonces != nil {
		consumed, err := proxy.options.Nonces.Consume(ctx, serviceNonceNamespace, claims.Nonce, claims.TTL)
		if err != nil {
			log.Printf("主机网关无法登记 nonce: %v", err)
			return nil, servicePath, method, errs.New(errs.RetServerSystemErr, "主机网关无法登记 nonce")
		}
		if !consumed {
			if proxy.options.Metrics != nil {
				proxy.options.Metrics.ReplayFailed()
			}
			return nil, servicePath, method, errs.New(gatewayroute.RetUnauthenticated, "请求被重放")
		}
	}
	response := &codec.Body{}
	timeout := time.Duration(route.TimeoutMS) * time.Millisecond
	upstreamCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	invokeOptions := []client.Option{
		client.WithTarget("ip://" + route.Address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(route.ServicePath), client.WithCalleeMethod(method),
		client.WithSerializationType(msg.SerializationType()), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithTimeout(timeout),
		// 可信调用方身份：入站的 x-moox- 元数据已全部丢弃，这个值只可能来自网关自己。
		client.WithMetaData(gatewayroute.MetadataVerifiedCaller, []byte(claims.Caller)),
	}
	if proxy.options.Catalog.IsReadOnly(route.ServicePath, method) {
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
	if err := client.New().Invoke(upstreamCtx, req, response, invokeOptions...); err != nil {
		proxy.upstreamFailed(err)
		return nil, servicePath, method, upstreamError(route, method, err)
	}
	if route.MaxBodyBytes > 0 && int64(len(response.Data)) > route.MaxBodyBytes {
		return nil, servicePath, method, errs.New(gatewayroute.RetBodyTooLarge, "响应体超过路由的包体上限")
	}
	return response, servicePath, method, nil
}

// rejectedMetricLabel 是被拒绝请求（调用方无权、方法不开放、服务不在本机）的指标标签。
const rejectedMetricLabel = "rejected"

func (proxy *nativeProxy) authFailed() {
	if proxy.options.Metrics != nil {
		proxy.options.Metrics.AuthFailed()
	}
}

func (proxy *nativeProxy) upstreamFailed(err error) {
	if proxy.options.Metrics == nil {
		return
	}
	var frameErr *errs.Error
	if errors.As(err, &frameErr) && frameErr.IsTimeout(errs.ErrorTypeFramework) {
		proxy.options.Metrics.UpstreamFailed("timeout")
		return
	}
	if code := errs.Code(err); code == errs.RetClientConnectFail || code == errs.RetClientNetErr {
		proxy.options.Metrics.UpstreamFailed("connection")
	}
}

// upstreamError 让上游超时仍能及时答复调用方。路由的超时会被 tRPC 视为全链路超时，而 tRPC 服务端
// 对除 RetClientTimeout 以外的框架超时都不回包，调用方就只能等到它自己更长的超时。
func upstreamError(route gatewayroute.Route, method string, err error) error {
	var frameErr *errs.Error
	if errors.As(err, &frameErr) && frameErr.IsTimeout(errs.ErrorTypeFramework) {
		return errs.NewFrameError(errs.RetClientTimeout, fmt.Sprintf(
			"主机网关上游 %s/%s 在 %dms 内没有响应: %s", route.ServicePath, method, route.TimeoutMS, frameErr.Msg))
	}
	return err
}

func splitRPCName(rpcName string) (string, string, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(rpcName), "/")
	servicePath, method, ok := strings.Cut(value, "/")
	return servicePath, method, ok && servicePath != "" && method != ""
}
