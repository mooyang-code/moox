package gatewayproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

var methodPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var requestHeaderAllowlist = []string{
	"Content-Type",
	"Accept-Encoding",
	"X-Trace-Id",
	"X-Space-Id",
}

var responseHeaderAllowlist = []string{
	"Content-Type",
	"Content-Encoding",
	"trpc-ret",
	"trpc-func-ret",
	"X-Trace-Id",
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func Forward(ctx context.Context, client *http.Client, route Route, method string, body []byte, headers http.Header) (*Response, error) {
	normalizeRouteDefaults(&route)
	if err := ValidateRoute(route); err != nil {
		return nil, fmt.Errorf("invalid route: %w", err)
	}
	if !methodPattern.MatchString(method) {
		return nil, fmt.Errorf("method %q must be a single URL-safe segment", method)
	}
	if int64(len(body)) > route.MaxBodyBytes {
		return nil, fmt.Errorf("request body is %d bytes, limit is %d", len(body), route.MaxBodyBytes)
	}

	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(route.TimeoutMS)*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "http://"+route.Address+"/"+route.ServicePath+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	copyHeaders(request.Header, headers, requestHeaderAllowlist)
	// An explicit empty value suppresses net/http's implicit User-Agent.
	request.Header["User-Agent"] = []string{""}

	httpClient := safeClient(client)
	upstream, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send upstream request: %w", err)
	}
	defer upstream.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(upstream.Body, route.MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upstream response body: %w", err)
	}
	if int64(len(responseBody)) > route.MaxBodyBytes {
		return nil, fmt.Errorf("upstream response body exceeds %d byte limit", route.MaxBodyBytes)
	}
	responseHeaders := make(http.Header)
	copyHeaders(responseHeaders, upstream.Header, responseHeaderAllowlist)
	return &Response{StatusCode: upstream.StatusCode, Header: responseHeaders, Body: responseBody}, nil
}

func safeClient(client *http.Client) *http.Client {
	if client != nil {
		copy := *client
		copy.Jar = nil
		if copy.Transport == nil {
			copy.Transport = safeTransport()
		}
		copy.CheckRedirect = rejectRedirect
		return &copy
	}
	return &http.Client{Transport: safeTransport(), CheckRedirect: rejectRedirect}
}

func safeTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	return transport
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func copyHeaders(destination, source http.Header, allowlist []string) {
	for _, key := range allowlist {
		for _, value := range source.Values(key) {
			destination.Add(key, value)
		}
	}
}
