package proxy

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/mooyang-code/moox/modules/egressproxy/internal/resolver"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	_ "github.com/mooyang-code/moox/packages/gatewayauth/nativewire"
	"github.com/mooyang-code/moox/packages/security/domainpolicy"
)

const (
	MaxResponseBytes = 32 << 20
	MaxRequestBytes  = 1 << 20
	maxHeaderBytes   = 32 << 10
	maxURLBytes      = 16 << 10
)

type Options struct {
	Domains  []string
	Resolver *resolver.Resolver
	// HTTPClient is an explicit fixture seam; production uses a direct,
	// TLS-verified transport with public-address checks and no environment proxy.
	HTTPClient *http.Client
}

type Proxy struct {
	domains  domainpolicy.Matcher
	resolver *resolver.Resolver
	client   *http.Client
}

func New(options Options) (*Proxy, error) {
	domains, err := domainpolicy.New(options.Domains)
	if err != nil {
		return nil, err
	}
	client := http.Client{Transport: &http.Transport{
		TLSClientConfig:    &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext:        publicDialer(net.DefaultResolver.LookupHost, (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext),
		DisableCompression: true, TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns: 64, MaxIdleConnsPerHost: 8, MaxConnsPerHost: 16,
		IdleConnTimeout: 90 * time.Second, MaxResponseHeaderBytes: maxHeaderBytes,
	}}
	if options.HTTPClient != nil {
		client = *options.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Proxy{domains: domains, resolver: options.Resolver, client: &client}, nil
}

func (p *Proxy) Close() { p.client.CloseIdleConnections() }

var requestHeaders = map[string]bool{"Accept": true, "Accept-Language": true, "Content-Type": true, "User-Agent": true}
var responseHeaders = map[string]bool{
	"Content-Type": true, "Cache-Control": true, "Etag": true, "Last-Modified": true,
	"Retry-After": true, "Expires": true, "Date": true,
	"X-Mbx-Used-Weight": true, "X-Mbx-Used-Weight-1m": true,
}

func filterRequestHeaders(values map[string]string) (http.Header, error) {
	if len(values) > 64 {
		return nil, errors.New("too many request headers")
	}
	output := http.Header{}
	total := 0
	for key, value := range values {
		total += len(key) + len(value)
		if total > maxHeaderBytes {
			return nil, errors.New("request headers exceed limit")
		}
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		if !requestHeaders[canonical] {
			continue
		}
		if len(output.Values(canonical)) != 0 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("invalid or duplicate request header")
		}
		output.Set(canonical, value)
	}
	// Decompression is owned here even when a fixture/client transport differs.
	output.Set("Accept-Encoding", "gzip, deflate, br")
	return output, nil
}

func ret(code commonpb.ErrorCode, message string) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: code, Msg: message}
}

func doError(code commonpb.ErrorCode, message string) (*egresspb.DoRsp, error) {
	return &egresspb.DoRsp{RetInfo: ret(code, message)}, nil
}

func (p *Proxy) Do(ctx context.Context, req *egresspb.DoReq) (*egresspb.DoRsp, error) {
	if req == nil || p == nil {
		return doError(commonpb.ErrorCode_INVALID_PARAM, "request and egress proxy are required")
	}
	if req.Method != http.MethodGet && req.Method != http.MethodPost {
		return doError(commonpb.ErrorCode_INVALID_PARAM, "egress permits GET and POST")
	}
	host, ok := domainpolicy.Host(req.Host)
	if !ok || !p.domains.Allows(host) {
		return doError(commonpb.ErrorCode_NO_PERMISSION, "egress domain is not allowed")
	}
	if req.TimeoutMs < 0 || req.TimeoutMs > 60000 || len(req.Body) > MaxRequestBytes || len(req.Path)+len(req.Query) > maxURLBytes {
		return doError(commonpb.ErrorCode_INVALID_PARAM, "egress request exceeds timeout, body or URL limit")
	}
	path, err := url.ParseRequestURI(req.Path)
	if err != nil || !strings.HasPrefix(req.Path, "/") || strings.HasPrefix(req.Path, "//") || strings.ContainsAny(req.Path, "\x00\r\n\\#?") || path.IsAbs() || path.Host != "" || path.RawQuery != "" || path.Fragment != "" || strings.ContainsAny(req.Query, "\x00\r\n#") {
		return doError(commonpb.ErrorCode_INVALID_PARAM, "egress requires a path and a separate encoded query")
	}
	if _, err := url.ParseQuery(req.Query); err != nil {
		return doError(commonpb.ErrorCode_INVALID_PARAM, "invalid encoded query")
	}
	headers, err := filterRequestHeaders(req.Headers)
	if err != nil {
		return doError(commonpb.ErrorCode_INVALID_PARAM, err.Error())
	}
	budget := time.Duration(req.TimeoutMs) * time.Millisecond
	if budget == 0 {
		budget = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	path.Scheme, path.Host, path.RawQuery = "https", host, req.Query
	request, err := http.NewRequestWithContext(ctx, req.Method, path.String(), bytes.NewReader(req.Body))
	if err != nil {
		return doError(commonpb.ErrorCode_INVALID_PARAM, "invalid HTTPS request")
	}
	request.Header = headers
	response, err := p.client.Do(request)
	if err != nil {
		return doError(commonpb.ErrorCode_INNER_ERR, "egress HTTPS request failed")
	}
	defer response.Body.Close()
	body, err := decodedBody(response)
	if err != nil {
		return doError(commonpb.ErrorCode_INNER_ERR, err.Error())
	}
	rspHeaders := map[string]string{}
	headerSize := 0
	for name, values := range response.Header {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if !responseHeaders[canonical] {
			continue
		}
		value := strings.Join(values, ", ")
		headerSize += len(canonical) + len(value)
		if headerSize > maxHeaderBytes || strings.ContainsAny(value, "\x00\r\n") {
			return doError(commonpb.ErrorCode_INNER_ERR, "egress response headers exceed limit or are invalid")
		}
		rspHeaders[canonical] = value
	}
	return &egresspb.DoRsp{RetInfo: ret(commonpb.ErrorCode_SUCCESS, ""), Status: int32(response.StatusCode), Headers: rspHeaders, Body: body}, nil
}

func decodedBody(response *http.Response) ([]byte, error) {
	var reader io.Reader = io.LimitReader(response.Body, (48<<20)+1)
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	var closer io.Closer
	var err error
	switch encoding {
	case "", "identity":
	case "gzip":
		reader, err = gzip.NewReader(reader)
		if err == nil {
			closer = reader.(io.Closer)
		}
	case "deflate":
		reader, err = zlib.NewReader(reader)
		if err == nil {
			closer = reader.(io.Closer)
		}
	case "br":
		reader = brotli.NewReader(reader)
	default:
		return nil, errors.New("unsupported egress response encoding")
	}
	if err != nil {
		return nil, errors.New("egress response decompression failed")
	}
	if closer != nil {
		defer closer.Close()
	}
	body, err := io.ReadAll(io.LimitReader(reader, MaxResponseBytes+1))
	if err != nil {
		return nil, errors.New("egress response read or decompression failed")
	}
	if len(body) > MaxResponseBytes {
		return nil, errors.New("egress decompressed response exceeds 32 MiB")
	}
	return body, nil
}

func (p *Proxy) ResolveDomains(ctx context.Context, req *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
	if req == nil {
		return &egresspb.ResolveDomainsRsp{RetInfo: ret(commonpb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	if p == nil || p.resolver == nil {
		return &egresspb.ResolveDomainsRsp{RetInfo: ret(commonpb.ErrorCode_INNER_ERR, "DNS resolver is unavailable")}, nil
	}
	results, err := p.resolver.Resolve(ctx, req.Domains, int(req.MaxIpsPerDomain))
	if err != nil {
		code := commonpb.ErrorCode_INNER_ERR
		if errors.Is(err, resolver.ErrInvalidDomain) {
			code = commonpb.ErrorCode_INVALID_PARAM
		}
		return &egresspb.ResolveDomainsRsp{RetInfo: ret(code, err.Error())}, nil
	}
	rsp := &egresspb.ResolveDomainsRsp{RetInfo: ret(commonpb.ErrorCode_SUCCESS, "")}
	for _, result := range results {
		if result.Unresolved || len(result.IPs) == 0 {
			rsp.UnresolvedDomains = append(rsp.UnresolvedDomains, result.Domain)
			continue
		}
		item := &egresspb.DomainResolution{Domain: result.Domain}
		for _, ip := range result.IPs {
			item.Ips = append(item.Ips, &egresspb.ResolvedIP{Ip: ip.IP, TcpConnectLatencyMs: ip.TCPConnectLatencyMS})
		}
		rsp.Resolutions = append(rsp.Resolutions, item)
	}
	return rsp, nil
}
