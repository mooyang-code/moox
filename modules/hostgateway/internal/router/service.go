package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	// 注册原生 tRPC 帧的压缩与包体上限：跨主机进来的请求是压缩过的，网关进程必须认得。
	_ "github.com/mooyang-code/moox/packages/gatewayauth/nativewire"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
)

// hostGatewayCaller 是每台主机网关签名时用的调用方名字。
const hostGatewayCaller = "host-gateway"

// replayGuard 是进程内的防重放检查（gatewayauth.Replay）。
type replayGuard interface {
	Accept(gatewayauth.Claims, time.Time) error
}
type Metrics interface {
	AuthFailed()
	ReplayFailed()
	UpstreamFailed(string)
	ObserveRequest(string, string, int, time.Duration)
}

type Forwarder interface {
	Forward(context.Context, servicecatalog.Route, int, []byte, codec.MetaData) ([]byte, error)
}

type ServiceOptions struct {
	State     *snapshot.State
	Replay    replayGuard
	Metrics   Metrics
	Now       func() time.Time
	Forwarder Forwarder
	// Remote 把外部入口收到、目标不在本机的请求转给目标主机；只有开了外部入口的网关才有。
	Remote RemoteForwarder
	// Registerer 登记外部入口的拒绝计数；不给时用默认的。
	Registerer prometheus.Registerer
}

type Proxy struct {
	options ServiceOptions
	catalog servicecatalog.Catalog
	owned   *Upstream
	denials *prometheus.CounterVec
}

func NewService(options ServiceOptions) (*Proxy, error) {
	if options.State == nil || options.Replay == nil {
		return nil, errors.New("host gateway requires snapshot and replay guard")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Registerer == nil {
		options.Registerer = prometheus.DefaultRegisterer
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	denials := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_gateway_external_denials_total",
		Help: "外部身份的请求在转发之前被拒绝的次数：外部入口上的，以及带来源头转过来、三条检查没过的。",
	}, []string{"caller", "service", "method", "reason"})
	if err := options.Registerer.Register(denials); err != nil {
		// 同一个进程里建第二个网关（测试）时沿用已登记的计数器。
		var registered prometheus.AlreadyRegisteredError
		if !errors.As(err, &registered) {
			return nil, fmt.Errorf("register host gateway metrics: %w", err)
		}
		existing, ok := registered.ExistingCollector.(*prometheus.CounterVec)
		if !ok {
			return nil, fmt.Errorf("register host gateway metrics: %w", err)
		}
		denials = existing
	}
	p := &Proxy{options: options, catalog: catalog, denials: denials}
	if p.options.Forwarder == nil {
		p.owned = NewUpstream()
		p.options.Forwarder = p.owned
	}
	return p, nil
}

func (p *Proxy) Close() error {
	if p.owned != nil {
		return p.owned.Close()
	}
	return nil
}

// Register 把网关挂到 11002、11003 的监听上：组件、moox-cli，以及入口主机的网关带来源头转过来的外部身份请求。
func (p *Proxy) Register(service server.Service) error {
	return register(service, p, p.handle)
}

func (p *Proxy) handle(_ interface{}, ctx context.Context, decode server.FilterFunc) (interface{}, error) {
	return p.dispatch(ctx, decode, p.serve)
}

func register(service server.Service, p *Proxy, handle func(interface{}, context.Context, server.FilterFunc) (interface{}, error)) error {
	return service.Register(&server.ServiceDesc{ServiceName: "trpc.moox.hostgateway.ServiceGateway", HandlerType: ((*interface{})(nil)),
		Methods: []server.Method{{Name: "*", Func: handle}},
	}, p)
}

// request 是一次进来的调用：路径拆出的服务与方法、原始请求体与全部元数据头。
type request struct {
	service, method string
	serialization   int
	compress        int
	body            []byte
	headers         http.Header
	inbound         codec.MetaData
}

// outcome 记一次调用在指标里的标签与状态。
type outcome struct {
	service, method string
	status          int
}

// dispatch 解出原始请求、记指标，把验签与转发交给 serve（11002、11003 用 serve，外部入口用 serveExternal）。
func (p *Proxy) dispatch(ctx context.Context, decode server.FilterFunc, serve func(context.Context, request, *outcome) ([]byte, error)) (interface{}, error) {
	raw := &codec.Body{}
	filters, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return filters.Filter(ctx, raw, func(ctx context.Context, input interface{}) (interface{}, error) {
		started := p.options.Now()
		out := &outcome{service: "unknown", method: "unknown", status: http.StatusBadRequest}
		defer func() {
			if p.options.Metrics != nil {
				p.options.Metrics.ObserveRequest(out.service, out.method, out.status, p.options.Now().Sub(started))
			}
		}()
		message := codec.Message(ctx)
		req := request{serialization: message.SerializationType(), compress: message.CompressType(), body: input.(*codec.Body).Data, headers: http.Header{}, inbound: message.ServerMetaData()}
		if req.serialization != codec.SerializationTypePB && req.serialization != codec.SerializationTypeJSON || len(req.body) > 64<<20 {
			return nil, errors.New("host gateway requires bounded PB or JSON body")
		}
		path := message.ServerRPCName()
		var ok bool
		req.service, req.method, ok = strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if !ok || path != "/"+req.service+"/"+req.method || req.service == "" || req.method == "" || strings.Contains(req.method, "/") {
			return nil, errors.New("invalid host gateway RPC path")
		}
		for key, value := range req.inbound {
			req.headers.Add(key, string(value))
		}
		response, err := serve(ctx, req, out)
		if err != nil {
			return nil, err
		}
		out.status = http.StatusOK
		return &codec.Body{Data: response}, nil
	})
}

// rejected 是网关自己拒签的专用错误：验签、时间窗、防重放、调用名单任何一步不过都在转发之前拒绝，
// 请求没有到达服务，调用方可以重新签名重发一次（写方法也一样）。原因只记指标，不回给调用方。
func (p *Proxy) rejected(out *outcome, status int, replayed bool) error {
	out.status = status
	if p.options.Metrics != nil {
		if replayed {
			p.options.Metrics.ReplayFailed()
		} else {
			p.options.Metrics.AuthFailed()
		}
	}
	return errs.NewFrameError(gatewayauth.RetRejected, "host gateway rejected the request signature or caller")
}

func (p *Proxy) serve(ctx context.Context, req request, out *outcome) ([]byte, error) {
	view := p.options.State.Load()
	if view == nil {
		return nil, errors.New("host gateway snapshot not initialized")
	}
	claims, err := view.Verify(gatewayauth.Request{TargetNode: view.HostID(), Callee: req.service, Func: req.method, Body: req.body}, req.headers, p.options.Now())
	if err != nil {
		return nil, p.rejected(out, http.StatusUnauthorized, false)
	}
	if err := p.options.Replay.Accept(claims, p.options.Now()); err != nil {
		return nil, p.rejected(out, http.StatusUnauthorized, true)
	}
	if view.Disabled() {
		out.status = http.StatusServiceUnavailable
		return nil, errors.New("host gateway is disabled")
	}
	route, ok := view.Resolve(req.service, req.method)
	if !ok {
		out.status = http.StatusNotFound
		// 使用框架级“服务不存在”错误码，调用方据此刷新服务目录并对只读请求重试到新主机。
		return nil, errs.NewFrameError(errs.RetServerNoService, "service/method is not deployed on this host")
	}
	out.service, out.method = route.ComponentID, req.method
	allowed := p.catalog.Allowed(claims.Caller, req.service, req.method) && slices.Contains(route.Callers, claims.Caller)
	if _, forwarded := req.headers[gatewayauth.HeaderOriginCaller]; forwarded {
		allowed = p.originAllowed(view, claims, req)
	}
	if !allowed {
		return nil, p.rejected(out, http.StatusForbidden, false)
	}
	if int64(len(req.body)) > route.MaxBodyBytes {
		out.status = http.StatusRequestEntityTooLarge
		return nil, errors.New("host gateway request exceeds route body limit")
	}
	return p.forwardLocal(ctx, route, req, upstreamMetadata(req.service, claims, req.inbound), out)
}

// originAllowed 是对来源头（X-Moox-Origin-Caller）的三条检查。每台主机都有 host-gateway，任何一台主机的私钥都能以它的身份签名，
// 所以缺一条就拒绝并计数：
//  1. 签名的是 host-gateway，并且签名主机在快照里登记成外部入口；
//  2. 头里的值是服务目录里的外部身份（moox-cli、console 与任何组件名都不行）；
//  3. 这个外部身份在被调方法的名单上。
func (p *Proxy) originAllowed(view *snapshot.View, claims gatewayauth.Claims, req request) bool {
	values := req.headers.Values(gatewayauth.HeaderOriginCaller)
	origin := ""
	if len(values) == 1 {
		origin = values[0]
	}
	reason := ""
	switch {
	case claims.Caller != hostGatewayCaller || !view.ExternalEntry(claims.Host):
		reason = "origin_host"
	case !p.catalog.ExternalIdentity(origin):
		reason = "origin_identity"
	case !p.catalog.PrincipalAllowed(origin, req.service, req.method):
		reason = "origin_method"
	default:
		return true
	}
	p.deny(origin, req.service, req.method, reason)
	return false
}

// deny 记一次对外部身份请求的拒绝。标签只取服务目录里有的值，别的一律记成 unknown，免得外面带进来的任意字符串撑大指标。
func (p *Proxy) deny(caller, service, method, reason string) {
	if !p.catalog.ExternalIdentity(caller) {
		caller = "unknown"
	}
	spec, ok := p.catalog.Service(service)
	if !ok {
		service, method = "unknown", "unknown"
	} else if _, found := spec.Method(method); !found {
		method = "unknown"
	}
	p.denials.WithLabelValues(caller, service, method, reason).Inc()
}

// forwardLocal 把请求转给本机的被调服务。
func (p *Proxy) forwardLocal(ctx context.Context, route servicecatalog.Route, req request, metadata codec.MetaData, out *outcome) ([]byte, error) {
	upstreamCtx, cancel := context.WithTimeout(ctx, time.Duration(route.TimeoutMS)*time.Millisecond)
	defer cancel()
	response, err := p.options.Forwarder.Forward(upstreamCtx, route, req.serialization, req.body, metadata)
	if err != nil {
		out.status = http.StatusBadGateway
		if p.options.Metrics != nil {
			p.options.Metrics.UpstreamFailed(failureKind(err))
		}
		return nil, routeError(route, err)
	}
	if int64(len(response)) > route.MaxBodyBytes {
		out.status = http.StatusBadGateway
		return nil, errors.New("host gateway response exceeds route body limit")
	}
	return response, nil
}

// upstreamMetadata 是转给本机被调服务的元数据：外面带进来的 x-moox- 头一律不往后传（签名到网关为止）；
// 只有 GatewayControl 另外收到网关写的、验过的签名主机 ID，它用这个头认是哪台主机的网关在调。
func upstreamMetadata(service string, claims gatewayauth.Claims, inbound codec.MetaData) codec.MetaData {
	metadata := codec.MetaData{}
	for key, value := range inbound {
		if !strings.HasPrefix(strings.ToLower(key), "x-moox-") {
			metadata[key] = value
		}
	}
	if service == servicecatalog.GatewayControlPath {
		metadata[gatewayauth.HeaderVerifiedHost] = []byte(claims.Host)
	}
	return metadata
}

func (p *Proxy) String() string {
	return fmt.Sprintf("HostGatewayProxy{initialized=%t}", p.options.State.Load() != nil)
}
