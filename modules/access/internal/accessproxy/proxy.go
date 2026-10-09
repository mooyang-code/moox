// Package accessproxy 是外部接入的转发入口：校验外部调用方（SCF、因子引擎、moox-skill）的签名，按组件目录中
// 外部调用方的白名单放行，再以 access 身份经本机主机网关把请求原样转发给目标服务。
package accessproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
)

// AccessServiceName 是外部接入入口（11004）的 tRPC 服务名。
const AccessServiceName = "trpc.moox.access.Access"

const (
	defaultMaxBodyBytes = 32 << 20
	defaultTimeout      = 30 * time.Second
	nonceNamespace      = "access"
	// unknownPrincipal 是签名未通过时指标中使用的调用方名。
	unknownPrincipal = "unknown"
)

// 拒绝原因，用作指标标签。
const (
	ReasonUnauthenticated = "unauthenticated"
	ReasonForbidden       = "forbidden"
	ReasonReplayed        = "replayed"
	ReasonTooLarge        = "too_large"
	ReasonUnavailable     = "unavailable"
)

// NonceStore 持久化入站 nonce，进程重启后重放窗口不会重新打开。
type NonceStore interface {
	Consume(context.Context, string, string, time.Duration) (bool, error)
}

// Upstream 是外部接入用到的 gatewayclient 能力：从服务目录取本机主机 ID，并以 access 身份转发请求。
type Upstream interface {
	Directory(ctx context.Context) (gatewayclient.View, error)
	Forward(ctx context.Context, servicePath, method string, serialization int, body []byte, opts ...gatewayclient.CallOption) ([]byte, error)
}

// Metrics 记录拒绝与转发结果。
type Metrics interface {
	Rejected(principal, reason string)
	Forwarded(principal, servicePath, method string, code int, elapsed time.Duration)
}

// Options 是转发入口的依赖。
type Options struct {
	Registry     *gatewayauth.CredentialRegistry
	Catalog      *servicecatalog.Catalog
	Upstream     Upstream
	Nonces       NonceStore
	Metrics      Metrics
	MaxBodyBytes int64
	Timeout      time.Duration
	Now          func() time.Time
}

// Proxy 是外部接入的转发实现。
type Proxy struct {
	registry     *gatewayauth.CredentialRegistry
	catalog      *servicecatalog.Catalog
	upstream     Upstream
	nonces       NonceStore
	metrics      Metrics
	maxBodyBytes int64
	timeout      time.Duration
	now          func() time.Time
}

// New 校验依赖并创建转发入口。
func New(options Options) (*Proxy, error) {
	if options.Registry == nil {
		return nil, errors.New("外部接入缺少外部调用方的校验密钥")
	}
	if options.Upstream == nil {
		return nil, errors.New("外部接入缺少上游 gatewayclient")
	}
	if options.Nonces == nil {
		return nil, errors.New("外部接入缺少 nonce 存储")
	}
	proxy := &Proxy{
		registry: options.Registry, catalog: options.Catalog, upstream: options.Upstream, nonces: options.Nonces,
		metrics: options.Metrics, maxBodyBytes: options.MaxBodyBytes, timeout: options.Timeout, now: options.Now,
	}
	if proxy.catalog == nil {
		proxy.catalog = servicecatalog.Default()
	}
	if proxy.maxBodyBytes <= 0 {
		proxy.maxBodyBytes = defaultMaxBodyBytes
	}
	if proxy.timeout <= 0 {
		proxy.timeout = defaultTimeout
	}
	if proxy.now == nil {
		proxy.now = time.Now
	}
	return proxy, nil
}

// Forward 校验外部调用方并把 PB 字节原样转发；返回的错误带网关错误码（4401、4403、4413、4503）或上游的错误码。
func (p *Proxy) Forward(ctx context.Context, request *codec.Body) (*codec.Body, error) {
	started := time.Now()
	principal, servicePath, method := unknownPrincipal, "", ""
	response, err := p.forward(ctx, request, &principal, &servicePath, &method)
	if p.metrics != nil && servicePath != "" {
		p.metrics.Forwarded(principal, servicePath, method, int(errs.Code(err)), time.Since(started))
	}
	return response, err
}

func (p *Proxy) forward(ctx context.Context, request *codec.Body, principal, servicePath, method *string) (*codec.Body, error) {
	if request == nil {
		return nil, errs.New(errs.RetServerDecodeFail, "外部接入请求体为空")
	}
	msg := codec.Message(ctx)
	service, rpc, ok := splitRPCName(msg.ServerRPCName())
	if !ok {
		return nil, errs.New(gatewayroute.RetServiceNotHere, fmt.Sprintf("无效的 RPC 名 %q", msg.ServerRPCName()))
	}
	*servicePath, *method = service, rpc
	if int64(len(request.Data)) > p.maxBodyBytes {
		p.rejected(*principal, ReasonTooLarge)
		return nil, errs.New(gatewayroute.RetBodyTooLarge, fmt.Sprintf("请求体超过 %d 字节", p.maxBodyBytes))
	}
	view, err := p.upstream.Directory(ctx)
	if err != nil || strings.TrimSpace(view.LocalHostID) == "" {
		p.rejected(*principal, ReasonUnavailable)
		return nil, errs.New(gatewayroute.RetHostDisabled, "外部接入还没有拿到本机的服务目录")
	}
	instanceID := servicecatalog.AccessCaller + "@" + view.LocalHostID
	metadata := msg.ServerMetaData()
	headers := make(http.Header, len(metadata))
	for key, value := range metadata {
		headers.Add(key, string(value))
	}
	claims, err := p.registry.Verify(gatewayauth.Request{
		Method: http.MethodPost, Path: "/" + service + "/" + rpc, TargetNode: instanceID,
		Callee: service, Func: rpc, Body: request.Data,
	}, headers, p.now())
	if err != nil {
		p.rejected(*principal, ReasonUnauthenticated)
		return nil, errs.New(gatewayroute.RetUnauthenticated, "外部调用方签名校验失败: "+err.Error())
	}
	*principal = claims.Caller
	if !p.catalog.PrincipalAllowed(claims.Caller, service, rpc) {
		p.rejected(*principal, ReasonForbidden)
		return nil, errs.New(gatewayroute.RetForbidden, fmt.Sprintf("外部调用方 %s 不能调用 %s/%s", claims.Caller, service, rpc))
	}
	consumed, err := p.nonces.Consume(ctx, nonceNamespace, claims.Nonce, claims.TTL)
	if err != nil {
		return nil, errs.New(errs.RetServerSystemErr, "外部接入无法登记 nonce: "+err.Error())
	}
	if !consumed {
		p.rejected(*principal, ReasonReplayed)
		return nil, errs.New(gatewayroute.RetUnauthenticated, "请求被重放")
	}

	timeout := p.timeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	opts := []gatewayclient.CallOption{
		gatewayclient.WithTimeout(timeout),
		gatewayclient.WithMetadata(gatewayroute.MetadataAccessPrincipal, []byte(claims.Caller)),
	}
	for key, value := range metadata {
		// 入站的签名头（x-moox-*）由 gatewayclient 丢弃，上游只认外部接入自己的 access 签名。
		if key == gatewayroute.MetadataAccessPrincipal {
			continue
		}
		opts = append(opts, gatewayclient.WithMetadata(key, value))
	}
	out, err := p.upstream.Forward(ctx, service, rpc, msg.SerializationType(), request.Data, opts...)
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > p.maxBodyBytes {
		return nil, errs.New(gatewayroute.RetBodyTooLarge, fmt.Sprintf("响应体超过 %d 字节", p.maxBodyBytes))
	}
	return &codec.Body{Data: out}, nil
}

func (p *Proxy) rejected(principal, reason string) {
	if p.metrics != nil {
		p.metrics.Rejected(principal, reason)
	}
}

func splitRPCName(rpcName string) (string, string, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(rpcName), "/")
	servicePath, method, ok := strings.Cut(value, "/")
	return servicePath, method, ok && servicePath != "" && method != "" && !strings.Contains(method, "/")
}

// AccessServiceDesc 是通配方法的服务描述：请求的 PB 字节不解码，原样转发。
var AccessServiceDesc = server.ServiceDesc{
	ServiceName: AccessServiceName,
	HandlerType: ((*AccessServer)(nil)),
	Methods:     []server.Method{{Name: "*", Func: accessForwardHandler}},
}

// AccessServer 是转发入口的接口。
type AccessServer interface {
	Forward(context.Context, *codec.Body) (*codec.Body, error)
}

// RegisterAccessService 把转发入口注册到外部接入的监听上。
func RegisterAccessService(s server.Service, impl AccessServer) error {
	if s == nil {
		return errors.New("外部接入服务未配置")
	}
	return s.Register(&AccessServiceDesc, impl)
}

func accessForwardHandler(svr interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
	req := &codec.Body{}
	filters, err := f(req)
	if err != nil {
		return nil, err
	}
	handle := func(ctx context.Context, req interface{}) (interface{}, error) {
		body, ok := req.(*codec.Body)
		if !ok {
			return nil, errs.New(errs.RetServerDecodeFail, "外部接入请求体无效")
		}
		return svr.(AccessServer).Forward(ctx, body)
	}
	return filters.Filter(ctx, req, handle)
}
