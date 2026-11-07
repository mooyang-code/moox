package httpclient

import (
	"context"
	"net/http"
)

type requestHeadersKey struct{}

// WithRequestHeaders 让经过这个上下文发出的请求带上额外的请求头（例如币安行情数据类接口的 X-MBX-APIKEY）。
// 请求头只随请求发出，日志与跟踪不记录它们的值；经出口代理时是否放行由两层白名单决定。
func WithRequestHeaders(ctx context.Context, headers http.Header) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return context.WithValue(ctx, requestHeadersKey{}, headers.Clone())
}

func applyRequestHeaders(ctx context.Context, req *http.Request) {
	headers, _ := ctx.Value(requestHeadersKey{}).(http.Header)
	for name, values := range headers {
		req.Header.Del(name)
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
}
