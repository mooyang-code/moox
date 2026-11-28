package router

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
)

// RemoteForwarder 是进程内的网关客户端：以 host-gateway 的身份、用本主机的密钥签名，按当前快照选目标主机。
type RemoteForwarder interface {
	Forward(ctx context.Context, service, method string, serialization int, body []byte) ([]byte, error)
}

// RegisterExternal 把外部入口（11004，明文加签名）挂到监听上。这个入口只接服务目录里的外部身份
// （scf-collector）：验签、防重放、调用名单与 11003 相同，
// 目标服务在本机就转本机，在别的主机就以 host-gateway 的身份带来源头转过去。
func (p *Proxy) RegisterExternal(service server.Service) error {
	return register(service, p, p.handleExternal)
}

func (p *Proxy) handleExternal(_ interface{}, ctx context.Context, decode server.FilterFunc) (interface{}, error) {
	return p.dispatch(ctx, decode, p.serveExternal)
}

func (p *Proxy) serveExternal(ctx context.Context, req request, out *outcome) ([]byte, error) {
	view := p.options.State.Load()
	if view == nil {
		return nil, errors.New("host gateway snapshot not initialized")
	}
	// denied 是不值得重签重发的拒绝（请求本身不对，或入口不可用）。
	denied := func(caller, reason, message string, status int) ([]byte, error) {
		out.status = status
		p.deny(caller, req.service, req.method, reason)
		return nil, errors.New(message)
	}
	// 登记里这台主机不是外部入口（或已停用）时一律不接：别的网关也不认它转过去的请求。
	if !view.ExternalEntry(view.HostID()) {
		return denied("", "not_entry", "host gateway external entry is not enabled for this host", http.StatusServiceUnavailable)
	}
	spec, _ := p.catalog.Service(req.service)
	if int64(len(req.body)) > spec.BodyLimit() {
		return denied("", "request_limit", "host gateway request exceeds service body limit", http.StatusRequestEntityTooLarge)
	}
	// 以下三步不过都返回拒签专用错误码：请求没有往后转，调用方可以重新签名重发一次。
	rejected := func(caller, reason string, replayed bool) ([]byte, error) {
		p.deny(caller, req.service, req.method, reason)
		return nil, p.rejected(out, http.StatusUnauthorized, replayed)
	}
	claims, err := view.Verify(gatewayauth.Request{TargetNode: view.HostID(), Callee: req.service, Func: req.method, Body: req.body}, req.headers, p.options.Now())
	if err != nil {
		return rejected("", "authentication", false)
	}
	// 只接外部身份：组件（带签名主机）与 moox-cli 不走这个入口。
	if claims.Host != "" || !p.catalog.PrincipalAllowed(claims.Caller, req.service, req.method) {
		return rejected(claims.Caller, "permission", false)
	}
	if err := p.options.Replay.Accept(claims, p.options.Now()); err != nil {
		return rejected(claims.Caller, "replay", true)
	}
	// 往后只带空间与追踪两个头，别的元数据（含全部 x-moox- 头）到此为止。
	metadata := gatewayclient.CallMetadata{}
	for name, output := range map[string]*string{"X-Space-Id": &metadata.SpaceID, "X-Trace-Id": &metadata.TraceID} {
		values := req.headers.Values(name)
		if len(values) > 1 || len(values) == 1 && (len(values[0]) > 512 || strings.ContainsAny(values[0], "\x00\r\n")) {
			return denied(claims.Caller, "metadata", "invalid external application metadata", http.StatusBadRequest)
		}
		if len(values) == 1 {
			*output = values[0]
		}
	}
	if route, local := view.Resolve(req.service, req.method); local {
		out.service, out.method = route.ComponentID, req.method
		forwarded := codec.MetaData{}
		for name, value := range map[string]string{"X-Space-Id": metadata.SpaceID, "X-Trace-Id": metadata.TraceID} {
			if value != "" {
				forwarded[name] = []byte(value)
			}
		}
		return p.forwardLocal(ctx, route, req, forwarded, out)
	}
	// 指标里跨主机转发的请求按被调服务与方法记（名单检查已过，两者都在组件目录里）。
	out.service, out.method = req.service, req.method
	if p.options.Remote == nil {
		out.status = http.StatusServiceUnavailable
		return nil, errors.New("host gateway external entry has no forwarder")
	}
	upstreamCtx := gatewayclient.WithCallMetadata(ctx, metadata)
	// 网关手里只有原始字节，不知道空间：沿用入站帧的压缩类型，调用方（SCF）选的空间字典在出站这一跳保持不变。
	upstreamCtx = gatewayclient.WithCompressType(upstreamCtx, req.compress)
	upstreamCtx = gatewayclient.WithOriginCaller(upstreamCtx, claims.Caller)
	upstreamCtx, cancel := context.WithTimeout(upstreamCtx, time.Duration(spec.TimeoutFor(req.method))*time.Millisecond)
	defer cancel()
	response, err := p.options.Remote.Forward(upstreamCtx, req.service, req.method, req.serialization, req.body)
	if err != nil {
		out.status = http.StatusBadGateway
		return nil, err
	}
	return response, nil
}
