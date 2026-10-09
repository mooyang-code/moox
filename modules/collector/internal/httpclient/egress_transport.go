package httpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security/domainpolicy"
)

const egressService = "trpc.moox.egress.Proxy"

// EgressTransport borrows the process's internal gateway client. The direct
// transport handles other domains; a failed proxy call never falls back to it.
type EgressTransport struct {
	gateway gatewayclient.Invoker
	domains domainpolicy.Matcher
	direct  http.RoundTripper
}

func NewEgressTransport(gateway gatewayclient.Invoker, domains []string, direct http.RoundTripper) (*EgressTransport, error) {
	if gateway == nil {
		return nil, errors.New("egress transport requires a shared gateway client")
	}
	matcher, err := domainpolicy.New(domains)
	if err != nil {
		return nil, err
	}
	if direct == nil {
		direct = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	}
	return &EgressTransport{gateway: gateway, domains: matcher, direct: direct}, nil
}

func NewEgressHTTPClient(gateway gatewayclient.Invoker, domains []string) (*HTTPClient, error) {
	client := NewHTTPClient()
	transport, err := NewEgressTransport(gateway, domains, client.httpClient.Transport)
	if err != nil {
		return nil, err
	}
	client.httpClient.Transport = transport
	client.httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && transport.domains.Allows(via[0].URL.Hostname()) {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return client, nil
}

func (t *EgressTransport) CloseIdleConnections() {
	if closer, ok := t.direct.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t *EgressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("HTTP request is required")
	}
	if req.URL == nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errors.New("HTTP request URL is required")
	}
	if !t.domains.Allows(req.URL.Hostname()) {
		return t.direct.RoundTrip(req)
	}
	if req.Body != nil {
		defer req.Body.Close()
	}
	if req.URL.User != nil || req.URL.Scheme != "https" || req.URL.Port() != "" && req.URL.Port() != "443" {
		return nil, errors.New("proxied domains require HTTPS on port 443")
	}
	if req.Method != http.MethodGet && req.Method != http.MethodPost {
		return nil, errors.New("egress transport permits GET and POST")
	}
	var body []byte
	if req.Body != nil {
		stop := context.AfterFunc(req.Context(), func() { _ = req.Body.Close() })
		defer stop()
		var err error
		body, err = io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
		if err != nil {
			if cause := req.Context().Err(); cause != nil {
				return nil, cause
			}
			return nil, fmt.Errorf("read egress request body: %w", err)
		}
		if len(body) > 1<<20 {
			return nil, errors.New("egress request body exceeds 1 MiB")
		}
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	headers := map[string]string{}
	for name, values := range req.Header {
		switch textproto.CanonicalMIMEHeaderKey(name) {
		case "Accept", "Accept-Language", "Content-Type", "User-Agent":
			headers[name] = strings.Join(values, ", ")
		}
	}
	budget := 60 * time.Second
	if deadline, ok := req.Context().Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return nil, context.DeadlineExceeded
	}
	timeoutMS := int32((budget + time.Millisecond - 1) / time.Millisecond)
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	var response egresspb.DoRsp
	if err := t.gateway.Invoke(req.Context(), egressService, "Do", &egresspb.DoReq{
		Method: req.Method, Host: req.URL.Hostname(), Path: path, Query: req.URL.RawQuery,
		Headers: headers, Body: body, TimeoutMs: timeoutMS,
	}, &response); err != nil {
		return nil, fmt.Errorf("egress proxy RPC: %w", err)
	}
	if response.RetInfo == nil {
		return nil, errors.New("egress proxy returned no status")
	}
	if response.RetInfo.Code != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("egress proxy failed: code=%d msg=%s", response.RetInfo.Code, response.RetInfo.Msg)
	}
	if response.Status < 100 || response.Status > 599 || len(response.Body) > 32<<20 {
		return nil, errors.New("egress proxy returned an invalid HTTP status or oversized body")
	}
	output := http.Header{}
	for name, value := range response.Headers {
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		switch canonical {
		case "Content-Type", "Cache-Control", "Etag", "Last-Modified", "Retry-After", "Expires", "Date":
		default:
			continue
		}
		if strings.ContainsAny(name+value, "\x00\r\n") {
			return nil, errors.New("invalid egress response header")
		}
		output.Set(canonical, value)
	}
	return &http.Response{
		StatusCode: int(response.Status), Status: fmt.Sprintf("%d %s", response.Status, http.StatusText(int(response.Status))),
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: output,
		Body: io.NopCloser(bytes.NewReader(response.Body)), ContentLength: int64(len(response.Body)),
		Uncompressed: true, Request: req,
	}, nil
}

// Close releases the HTTP transports; the process closes its borrowed gateway
// only after all workers using these transports have stopped.
func (c *HTTPClient) Close() {
	if c == nil {
		return
	}
	c.ipMu.Lock()
	defer c.ipMu.Unlock()
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	for _, client := range c.ipClients {
		client.CloseIdleConnections()
	}
}
