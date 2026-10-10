package httpclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

// EgressDoer 是出口代理 Do 调用的最小接口，生产中是 egresspb.ProxyClientProxy。
type EgressDoer interface {
	Do(ctx context.Context, req *egresspb.DoReq, opts ...client.Option) (*egresspb.DoRsp, error)
}

const (
	// egressRequestTimeout 是经出口代理的客户端的整体超时：多了一跳跨地域往返，且 exchangeInfo 这类响应较大。
	egressRequestTimeout = 60 * time.Second
	// egressMaxTimeout 是出口代理允许的单次请求最长超时。
	egressMaxTimeout = 60 * time.Second
	// egressRPCMargin 留给出口代理回到 Collector 的这一跳：出口代理对目标的超时比调用方的截止时间早这么久，
	// 目标超时时由出口代理返回明确的错误，而不是整个调用一起超时。
	egressRPCMargin = 500 * time.Millisecond
	// maxEgressRequestBody 是经出口代理发送的请求体上限。
	maxEgressRequestBody = 1 << 20
	// maxEgressResponseBody 是解压后的响应体上限，与出口代理的默认上限一致。
	maxEgressResponseBody = 32 << 20
	// egressBodyEncodingHeader 是出口代理标记响应体已压缩的响应头（proxy.BodyEncodingHeader）。
	egressBodyEncodingHeader = "X-Moox-Body-Encoding"
)

// EgressTransport 把白名单域名的请求转成出口代理的 Do 调用，再把结果还原成 http.Response；其他域名交给
// direct 直连。目标返回的状态码（包括 429、5xx）原样交给上层现有的重试逻辑。
type EgressTransport struct {
	domains egresspb.DomainList
	proxy   EgressDoer
	direct  http.RoundTripper
}

// NewEgressTransport 创建出口代理传输层。
func NewEgressTransport(domains egresspb.DomainList, proxy EgressDoer, direct http.RoundTripper) *EgressTransport {
	return &EgressTransport{domains: domains, proxy: proxy, direct: direct}
}

// NewEgressHTTPClient 返回经出口代理访问白名单域名的客户端，其他域名照常直连。它不跟随重定向，以免重定向把
// 请求带到白名单之外的域名上直连出去。
func NewEgressHTTPClient(domains egresspb.DomainList, proxy EgressDoer) *HTTPClient {
	return &HTTPClient{httpClient: &http.Client{
		Timeout:       egressRequestTimeout,
		Transport:     NewEgressTransport(domains, proxy, newDirectTransport()),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// RoundTrip 实现 http.RoundTripper。
func (t *EgressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.domains.Allows(req.URL.Hostname()) {
		return t.direct.RoundTrip(req)
	}
	doReq, err := buildEgressRequest(req)
	if req.Body != nil {
		_ = req.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	rsp, err := t.proxy.Do(req.Context(), doReq)
	if err != nil {
		return nil, fmt.Errorf("经出口代理请求 %s 失败: %w", doReq.GetHost(), err)
	}
	if rsp.GetRetInfo() == nil {
		return nil, fmt.Errorf("经出口代理请求 %s 失败: 出口代理返回了空响应", doReq.GetHost())
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("经出口代理请求 %s 失败（%s）: %s", doReq.GetHost(), code, rsp.GetRetInfo().GetMsg())
	}
	if rsp.GetStatus() < 100 || rsp.GetStatus() > 999 {
		return nil, fmt.Errorf("经出口代理请求 %s 失败: 状态码 %d 无效", doReq.GetHost(), rsp.GetStatus())
	}
	return egressResponse(req, rsp)
}

// buildEgressRequest 把 http.Request 转成 DoReq。出口代理只发 443 端口的 HTTPS 请求。
func buildEgressRequest(req *http.Request) (*egresspb.DoReq, error) {
	host := egresspb.NormalizeHost(req.URL.Hostname())
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("出口代理只发 HTTPS 请求，%s 的请求是 %s", host, req.URL.Scheme)
	}
	if port := req.URL.Port(); port != "" && port != "443" {
		return nil, fmt.Errorf("出口代理只访问 443 端口，%s 的请求是 %s 端口", host, port)
	}
	timeout, err := egressTimeout(req.Context())
	if err != nil {
		return nil, err
	}
	var body []byte
	if req.Body != nil && req.Body != http.NoBody {
		body, err = io.ReadAll(io.LimitReader(req.Body, maxEgressRequestBody+1))
		if err != nil {
			return nil, fmt.Errorf("读取发往 %s 的请求体失败: %w", host, err)
		}
		if len(body) > maxEgressRequestBody {
			return nil, fmt.Errorf("发往 %s 的请求体超过 %d 字节", host, maxEgressRequestBody)
		}
	}
	headers := make(map[string]string, len(req.Header))
	for name := range req.Header {
		headers[name] = req.Header.Get(name)
	}
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	return &egresspb.DoReq{
		Method: req.Method, Host: host, Path: path, Query: req.URL.RawQuery,
		Headers: headers, Body: body, TimeoutMs: int32(timeout / time.Millisecond),
	}, nil
}

// egressTimeout 按请求的截止时间算出出口代理对目标的超时；没有截止时间时返回 0，由出口代理使用默认超时。
func egressTimeout(ctx context.Context) (time.Duration, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, nil
	}
	remaining := time.Until(deadline)
	if remaining < time.Millisecond {
		return 0, context.DeadlineExceeded
	}
	if remaining > 2*egressRPCMargin {
		remaining -= egressRPCMargin
	}
	return min(remaining, egressMaxTimeout), nil
}

// egressResponse 把 DoRsp 还原成 http.Response。响应体已由出口代理解压并完整读出，所以去掉描述传输方式的响应头；
// 出口代理为跨地域传输压缩过的响应体在这里还原，解压后的长度受 maxEgressResponseBody 限制。
func egressResponse(req *http.Request, rsp *egresspb.DoRsp) (*http.Response, error) {
	header := make(http.Header, len(rsp.GetHeaders()))
	compressed := false
	for name, value := range rsp.GetHeaders() {
		switch http.CanonicalHeaderKey(name) {
		case "Content-Length", "Content-Encoding", "Transfer-Encoding", "Connection":
			continue
		case egressBodyEncodingHeader:
			compressed = strings.EqualFold(value, "gzip")
			continue
		}
		header.Set(name, value)
	}
	body := rsp.GetBody()
	if compressed {
		var err error
		if body, err = gunzipLimited(body, maxEgressResponseBody); err != nil {
			return nil, fmt.Errorf("经出口代理请求 %s 失败: 响应体解压失败: %w", req.URL.Hostname(), err)
		}
	}
	status := int(rsp.GetStatus())
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status,
		Proto:      "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

// gunzipLimited 解压 gzip 数据，解压后超过 limit 字节时返回错误，防止压缩炸弹。
func gunzipLimited(data []byte, limit int64) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	out, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("解压后超过 %d 字节", limit)
	}
	return out, nil
}
