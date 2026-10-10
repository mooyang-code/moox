// Package proxy 实现出口代理的 tRPC 服务 trpc.moox.egress.Proxy：Do 替调用方发出 HTTPS 请求并把响应原样返回，
// ResolveDomains 解析白名单域名供 SCF 的 DNS 快照使用。
package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/egressproxy/internal/resolver"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/log"
)

// ServiceName 是出口代理在主机网关后面的 tRPC 服务名。
const ServiceName = "trpc.moox.egress.Proxy"

const (
	// DefaultMaxResponseBytes 是响应体（解压后）的默认上限。
	DefaultMaxResponseBytes int64 = 32 << 20
	// MaxTimeout 是单次请求允许的最长超时。
	MaxTimeout = 60 * time.Second
	// DefaultTimeout 是请求没有指定超时时使用的超时。
	DefaultTimeout = 15 * time.Second
	// BodyEncodingHeader 标记响应体已被代理 gzip 压缩；跨地域传输时，exchangeInfo 这类十几 MB 的 JSON 压缩后只有约十分之一。
	BodyEncodingHeader = "X-Moox-Body-Encoding"
	// compressMinBytes 是压缩响应体的最小长度，更小的响应压缩得不偿失。
	compressMinBytes = 64 << 10
)

// DefaultHeaders 是默认允许转发的请求头。
var DefaultHeaders = []string{"Accept", "Content-Type", "User-Agent"}

// Config 是出口代理的 HTTP 设置。
type Config struct {
	// Domains 是允许访问的域名白名单，支持 "*." 前缀。
	Domains []string
	// Headers 是允许转发的请求头，不区分大小写；为空时使用 DefaultHeaders。
	Headers []string
	// MaxResponseBytes 是响应体（解压后）的上限；为 0 时使用 DefaultMaxResponseBytes。
	MaxResponseBytes int64
	// DefaultTimeout 是请求没有指定超时时使用的超时；为 0 时使用 DefaultTimeout。
	DefaultTimeout time.Duration
	// Transport 发出 HTTPS 请求；为空时使用不读代理环境变量的默认传输层。测试可替换。
	Transport http.RoundTripper
}

// Server 实现 egresspb.ProxyService。
type Server struct {
	domains        egresspb.DomainList
	headers        map[string]struct{}
	client         *http.Client
	maxBody        int64
	defaultTimeout time.Duration
	resolver       *resolver.Resolver
	metrics        *Metrics
	inflight       chan struct{}
}

// New 创建出口代理。dns 为空时 ResolveDomains 返回错误；metrics 可以为空。
func New(cfg Config, dns *resolver.Resolver, metrics *Metrics) (*Server, error) {
	domains, err := egresspb.ParseDomainList(cfg.Domains)
	if err != nil {
		return nil, err
	}
	if domains.Empty() {
		return nil, errors.New("域名白名单不能为空")
	}
	headerNames := cfg.Headers
	if len(headerNames) == 0 {
		headerNames = DefaultHeaders
	}
	headers := make(map[string]struct{}, len(headerNames))
	for _, name := range headerNames {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonical == "" || strings.EqualFold(canonical, "Accept-Encoding") || strings.EqualFold(canonical, "Host") {
			return nil, fmt.Errorf("请求头 %q 不能加入白名单", name)
		}
		headers[canonical] = struct{}{}
	}
	maxBody := cfg.MaxResponseBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxResponseBytes
	}
	timeout := cfg.DefaultTimeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		return nil, fmt.Errorf("默认超时不能超过 %s", MaxTimeout)
	}
	transport := cfg.Transport
	if transport == nil {
		transport = defaultTransport()
	}
	return &Server{
		domains: domains, headers: headers, maxBody: maxBody, defaultTimeout: timeout, resolver: dns, metrics: metrics,
		inflight: make(chan struct{}, maxInflight),
		client: &http.Client{
			Transport: transport,
			// 不跟随重定向：重定向的目标可能不在白名单内，把 3xx 原样交给调用方处理。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// defaultTransport 是不读 HTTPS_PROXY 等环境变量的 HTTPS 传输层：出口代理本身就是出口。
func defaultTransport() *http.Transport {
	return &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		IdleConnTimeout:     90 * time.Second,
	}
}

// requestFailure 返回请求失败的原因，去掉 url.Error 里带 query 的完整 URL。
func requestFailure(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err.Error()
	}
	return err.Error()
}

// maxInflight 是同时在途的请求数上限：Collector 的重试风暴不能把 compute-1 上其它服务（交易）的资源占满。
const maxInflight = 32

// Do 替调用方发出一次 HTTPS 请求。
func (s *Server) Do(ctx context.Context, req *egresspb.DoReq) (*egresspb.DoRsp, error) {
	select {
	case s.inflight <- struct{}{}:
		defer func() { <-s.inflight }()
	default:
		s.metrics.rejected("busy")
		return &egresspb.DoRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, "出口代理繁忙，请稍后重试")}, nil
	}
	started := time.Now()
	request, timeout, rejected := s.buildRequest(ctx, req)
	if rejected != nil {
		s.metrics.rejected(rejected.reason)
		log.WarnContextf(ctx, "egress_proxy_rejected host=%q reason=%s msg=%q", req.GetHost(), rejected.reason, rejected.info.GetMsg())
		return &egresspb.DoRsp{RetInfo: rejected.info}, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := s.client.Do(request.WithContext(callCtx))
	if err != nil {
		s.metrics.forwarded(request.URL.Hostname(), "error", time.Since(started))
		// url.Error 的文本带完整的 URL（含 query）：目前只代理公开行情接口，以后若用于带签名的请求就会把签名写进日志
		// 和返回给调用方的错误里，所以只取里面真正的错误。
		reason := requestFailure(err)
		log.WarnContextf(ctx, "egress_proxy_failed host=%s path=%q duration_ms=%d error=%s", request.URL.Hostname(), request.URL.Path, time.Since(started).Milliseconds(), reason)
		return &egresspb.DoRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, fmt.Sprintf("请求 https://%s%s 失败: %s", request.URL.Host, request.URL.Path, reason))}, nil
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, s.maxBody+1))
	if err != nil {
		s.metrics.forwarded(request.URL.Hostname(), "error", time.Since(started))
		return &egresspb.DoRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, fmt.Sprintf("读取 https://%s%s 的响应失败: %v", request.URL.Host, request.URL.Path, err))}, nil
	}
	if int64(len(body)) > s.maxBody {
		s.metrics.forwarded(request.URL.Hostname(), "too_large", time.Since(started))
		return &egresspb.DoRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, fmt.Sprintf("https://%s%s 的响应超过 %d 字节", request.URL.Host, request.URL.Path, s.maxBody))}, nil
	}
	s.metrics.forwarded(request.URL.Hostname(), fmt.Sprint(response.StatusCode), time.Since(started))
	log.InfoContextf(ctx, "egress_proxy_done method=%s host=%s path=%q status=%d bytes=%d duration_ms=%d", request.Method, request.URL.Hostname(), request.URL.Path, response.StatusCode, len(body), time.Since(started).Milliseconds())
	headers := flattenHeaders(response.Header)
	body, headers = compressBody(body, headers)
	return &egresspb.DoRsp{
		RetInfo: retInfo(commonpb.ErrorCode_SUCCESS, ""),
		Status:  int32(response.StatusCode), Headers: headers, Body: body,
	}, nil
}

// compressBody 压缩较大的响应体并在响应头里标记；压缩失败或没有变小时原样返回。
func compressBody(body []byte, headers map[string]string) ([]byte, map[string]string) {
	if len(body) < compressMinBytes {
		return body, headers
	}
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestSpeed)
	if err != nil {
		return body, headers
	}
	if _, err := writer.Write(body); err != nil || writer.Close() != nil || buffer.Len() >= len(body) {
		return body, headers
	}
	headers[BodyEncodingHeader] = "gzip"
	return buffer.Bytes(), headers
}

type rejection struct {
	reason string
	info   *commonpb.RetInfo
}

func reject(reason string, code commonpb.ErrorCode, format string, args ...any) *rejection {
	return &rejection{reason: reason, info: retInfo(code, fmt.Sprintf(format, args...))}
}

// buildRequest 校验请求并构造发往目标的 HTTPS 请求。
func (s *Server) buildRequest(ctx context.Context, req *egresspb.DoReq) (*http.Request, time.Duration, *rejection) {
	if req == nil {
		return nil, 0, reject("invalid", commonpb.ErrorCode_INVALID_PARAM, "请求不能为空")
	}
	method := strings.ToUpper(strings.TrimSpace(req.GetMethod()))
	if method != http.MethodGet && method != http.MethodPost {
		return nil, 0, reject("invalid", commonpb.ErrorCode_INVALID_PARAM, "method 只支持 GET 和 POST")
	}
	host := strings.ToLower(strings.TrimSpace(req.GetHost()))
	if !egresspb.ValidDomain(host) {
		return nil, 0, reject("invalid", commonpb.ErrorCode_INVALID_PARAM, "host %q 必须是不带端口的域名", req.GetHost())
	}
	if !s.domains.Allows(host) {
		return nil, 0, reject("domain_not_allowed", commonpb.ErrorCode_NO_PERMISSION, "域名 %s 不在出口代理的白名单内", host)
	}
	path := req.GetPath()
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\r\n ") || strings.ContainsAny(req.GetQuery(), "#\r\n ") {
		return nil, 0, reject("invalid", commonpb.ErrorCode_INVALID_PARAM, "path 必须以 / 开头，path 与 query 不能包含空白、? 或 #")
	}
	timeout := s.defaultTimeout
	if req.GetTimeoutMs() > 0 {
		timeout = time.Duration(req.GetTimeoutMs()) * time.Millisecond
	}
	if timeout > MaxTimeout {
		return nil, 0, reject("invalid", commonpb.ErrorCode_INVALID_PARAM, "timeout_ms 不能超过 %d", MaxTimeout.Milliseconds())
	}
	if method == http.MethodGet && len(req.GetBody()) > 0 {
		return nil, 0, reject("invalid", commonpb.ErrorCode_INVALID_PARAM, "GET 请求不能带 body")
	}
	target := &url.URL{Scheme: "https", Host: host, RawQuery: req.GetQuery()}
	unescaped, err := url.PathUnescape(path)
	if err != nil {
		return nil, 0, reject("invalid", commonpb.ErrorCode_INVALID_PARAM, "path 编码无效: %v", err)
	}
	target.Path, target.RawPath = unescaped, path
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(req.GetBody()))
	if err != nil {
		return nil, 0, reject("invalid", commonpb.ErrorCode_INVALID_PARAM, "构造请求失败: %v", err)
	}
	for name, value := range req.GetHeaders() {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if _, allowed := s.headers[canonical]; !allowed || strings.ContainsAny(value, "\r\n") {
			continue
		}
		request.Header.Set(canonical, value)
	}
	return request, timeout, nil
}

// flattenHeaders 把响应头展开成一个值一行；同名的多个值用 ", " 拼接。
func flattenHeaders(header http.Header) map[string]string {
	out := make(map[string]string, len(header))
	for name, values := range header {
		out[name] = strings.Join(values, ", ")
	}
	return out
}

// ResolveDomains 解析白名单域名，返回经过 TCP 探测的地址。
func (s *Server) ResolveDomains(ctx context.Context, req *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
	started := time.Now()
	if req == nil {
		return &egresspb.ResolveDomainsRsp{RetInfo: retInfo(commonpb.ErrorCode_INVALID_PARAM, "请求不能为空")}, nil
	}
	if s.resolver == nil {
		return &egresspb.ResolveDomainsRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, "出口代理没有启用 DNS 解析")}, nil
	}
	results, err := s.resolver.Resolve(ctx, req.GetDomains(), int(req.GetMaxIpsPerDomain()))
	if err != nil {
		if errors.Is(err, resolver.ErrInvalidDomain) {
			return &egresspb.ResolveDomainsRsp{RetInfo: retInfo(commonpb.ErrorCode_INVALID_PARAM, err.Error())}, nil
		}
		return &egresspb.ResolveDomainsRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &egresspb.ResolveDomainsRsp{RetInfo: retInfo(commonpb.ErrorCode_SUCCESS, "")}
	for _, result := range results {
		if result.Unresolved || len(result.IPs) == 0 {
			rsp.UnresolvedDomains = append(rsp.UnresolvedDomains, result.Domain)
			continue
		}
		item := &egresspb.DomainResolution{Domain: result.Domain}
		for _, resolved := range result.IPs {
			item.Ips = append(item.Ips, &egresspb.ResolvedIP{Ip: resolved.IP, TcpConnectLatencyMs: resolved.TCPConnectLatencyMS})
		}
		rsp.Resolutions = append(rsp.Resolutions, item)
	}
	log.InfoContextf(ctx, "egress_dns_resolve_domains domains=%d resolutions=%d unresolved=%d duration_ms=%d", len(req.GetDomains()), len(rsp.GetResolutions()), len(rsp.GetUnresolvedDomains()), time.Since(started).Milliseconds())
	return rsp, nil
}

func retInfo(code commonpb.ErrorCode, msg string) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: code, Msg: msg}
}
