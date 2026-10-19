package proxy

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/mooyang-code/moox/modules/egressproxy/internal/resolver"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func tlsFixture(t *testing.T, handler http.HandlerFunc) (*Proxy, *http.Transport) {
	t.Helper()
	upstream := httptest.NewTLSServer(handler)
	t.Cleanup(upstream.Close)
	endpoint, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = endpoint.Hostname()
	transport.DisableCompression = true
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, endpoint.Host)
	}
	handlerProxy, err := New(Options{Domains: []string{"*.binance.com"}, HTTPClient: &http.Client{Transport: transport}})
	require.NoError(t, err)
	t.Cleanup(handlerProxy.Close)
	return handlerProxy, transport
}

func request() *egresspb.DoReq {
	return &egresspb.DoReq{Method: "GET", Host: "api.binance.com", Path: "/api/v3/exchangeInfo", Query: "symbol=A%2FB&empty=&plus=a+b", TimeoutMs: 1000}
}

func TestHTTPSPreservesStatusBodyAndFiltersHeaders(t *testing.T) {
	for _, status := range []int{200, 429, 500, 503} {
		for _, encoding := range []string{"identity", "gzip", "deflate", "br"} {
			t.Run(http.StatusText(status)+"/"+encoding, func(t *testing.T) {
				capturedRequests := make(chan *http.Request, 1)
				payload := []byte(`{"symbols":["BTCUSDT"]}`)
				p, _ := tlsFixture(t, func(w http.ResponseWriter, r *http.Request) {
					capturedRequests <- r.Clone(context.Background())
					w.Header().Set("Content-Encoding", encoding)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "3")
					w.Header().Set("X-Mbx-Used-Weight-1m", "9")
					w.Header().Set("Set-Cookie", "private=value")
					w.Header().Set("Connection", "keep-alive")
					w.WriteHeader(status)
					writer := io.Writer(w)
					var closer io.Closer
					switch encoding {
					case "gzip":
						zipper := gzip.NewWriter(w)
						writer, closer = zipper, zipper
					case "deflate":
						zipper := zlib.NewWriter(w)
						writer, closer = zipper, zipper
					case "br":
						zipper := brotli.NewWriter(w)
						writer, closer = zipper, zipper
					}
					_, _ = writer.Write(payload)
					if closer != nil {
						_ = closer.Close()
					}
				})
				req := request()
				req.Method, req.Body = "POST", []byte("input")
				req.Headers = map[string]string{"Accept": "application/json", "Content-Type": "application/json", "Authorization": "secret", "Cookie": "secret", "X-Space-Id": "space", "Host": "evil.test", "Accept-Encoding": "evil", "Connection": "upgrade"}
				rsp, err := p.Do(context.Background(), req)
				require.NoError(t, err)
				require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
				require.Equal(t, int32(status), rsp.Status)
				require.Equal(t, payload, rsp.Body)
				captured := <-capturedRequests
				require.Equal(t, "api.binance.com", captured.Host)
				require.Equal(t, req.Path+"?"+req.Query, captured.RequestURI)
				require.Equal(t, "application/json", captured.Header.Get("Accept"))
				require.Equal(t, "gzip, deflate, br", captured.Header.Get("Accept-Encoding"))
				for _, header := range []string{"Authorization", "Cookie", "X-Space-Id", "Connection"} {
					require.Empty(t, captured.Header.Get(header), header)
				}
				require.Equal(t, "3", rsp.Headers["Retry-After"])
				require.Equal(t, "9", rsp.Headers["X-Mbx-Used-Weight-1m"])
				for _, header := range []string{"Content-Encoding", "Content-Length", "Set-Cookie", "Connection"} {
					require.NotContains(t, rsp.Headers, header)
				}
			})
		}
	}
}

func TestRejectsRequestsBeforeSendingAndDoesNotFollowRedirects(t *testing.T) {
	var calls atomic.Int32
	p, _ := tlsFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "http://127.0.0.1/private")
		w.WriteHeader(http.StatusFound)
	})
	invalid := []func(*egresspb.DoReq){
		func(r *egresspb.DoReq) { r.Method = "CONNECT" },
		func(r *egresspb.DoReq) { r.Host = "api.binance.com.evil" },
		func(r *egresspb.DoReq) { r.Host = "api.binance.com:443" },
		func(r *egresspb.DoReq) { r.Host = "127.0.0.1" },
		func(r *egresspb.DoReq) { r.Path = "https://api.binance.com/x" },
		func(r *egresspb.DoReq) { r.Path = "//evil.test/x" },
		func(r *egresspb.DoReq) { r.Path = "/x?y=z" },
		func(r *egresspb.DoReq) { r.Query = "x=%XX" },
		func(r *egresspb.DoReq) { r.Query = "x=1#hidden" },
		func(r *egresspb.DoReq) { r.Headers = map[string]string{"Accept": "x\r\nInjected: yes"} },
		func(r *egresspb.DoReq) { r.Headers = map[string]string{"Accept": "a", "accept": "b"} },
		func(r *egresspb.DoReq) { r.TimeoutMs = 60001 },
		func(r *egresspb.DoReq) { r.Body = make([]byte, MaxRequestBytes+1) },
	}
	for _, mutate := range invalid {
		req := request()
		mutate(req)
		rsp, err := p.Do(context.Background(), req)
		require.NoError(t, err)
		require.NotEqual(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		require.Empty(t, rsp.Body)
	}
	require.Zero(t, calls.Load())
	rsp, err := p.Do(context.Background(), request())
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, int32(http.StatusFound), rsp.Status)
	require.Equal(t, int32(1), calls.Load())
}

func TestTLSVerificationAndContextDeadline(t *testing.T) {
	p, transport := tlsFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	transport.TLSClientConfig.RootCAs = x509.NewCertPool()
	rsp, err := p.Do(context.Background(), request())
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	var deadline time.Duration
	client, err := New(Options{Domains: []string{"*.binance.com"}, HTTPClient: &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		limit, ok := r.Context().Deadline()
		if ok {
			deadline = time.Until(limit)
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}})
	require.NoError(t, err)
	t.Cleanup(client.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	rsp, err = client.Do(ctx, request())
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Positive(t, deadline)
	require.LessOrEqual(t, deadline, 20*time.Millisecond)
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestDecodedLimitRejectsCompressedExpansionAndMalformedEncoding(t *testing.T) {
	var compressed bytes.Buffer
	zipper := gzip.NewWriter(&compressed)
	_, err := io.CopyN(zipper, zeroReader{}, MaxResponseBytes+1)
	require.NoError(t, err)
	require.NoError(t, zipper.Close())
	for _, fixture := range []struct {
		encoding string
		body     []byte
	}{
		{"gzip", compressed.Bytes()}, {"gzip", []byte("broken")}, {"deflate", []byte("broken")}, {"unknown", []byte("data")},
	} {
		response := &http.Response{Header: http.Header{"Content-Encoding": []string{fixture.encoding}}, Body: io.NopCloser(bytes.NewReader(fixture.body))}
		body, err := decodedBody(response)
		require.Error(t, err)
		require.Empty(t, body)
	}
	body, err := decodedBody(&http.Response{Header: http.Header{}, Body: io.NopCloser(io.LimitReader(zeroReader{}, MaxResponseBytes))})
	require.NoError(t, err)
	require.Len(t, body, MaxResponseBytes)
}

func TestDNSRPCPreservesSortedResultsCacheAndUnresolvedDomains(t *testing.T) {
	var lookups atomic.Int32
	dns := resolver.New(resolver.Config{Domains: []string{"api.binance.com"}, LookupHost: func(context.Context, string) ([]string, error) {
		lookups.Add(1)
		return []string{"8.8.4.4", "127.0.0.1", "8.8.8.8", "8.8.4.4"}, nil
	}, DialContext: func(context.Context, string, string) (net.Conn, error) {
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}})
	p, err := New(Options{Resolver: dns})
	require.NoError(t, err)
	t.Cleanup(p.Close)
	for i := 0; i < 2; i++ {
		rsp, err := p.ResolveDomains(context.Background(), &egresspb.ResolveDomainsReq{Domains: []string{"missing.test", "API.BINANCE.COM.", "api.binance.com"}, MaxIpsPerDomain: 1})
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		require.Equal(t, []string{"missing.test"}, rsp.UnresolvedDomains)
		require.Len(t, rsp.Resolutions, 1)
		require.Equal(t, "api.binance.com", rsp.Resolutions[0].Domain)
		require.Len(t, rsp.Resolutions[0].Ips, 1)
		require.Contains(t, []string{"8.8.4.4", "8.8.8.8"}, rsp.Resolutions[0].Ips[0].Ip)
	}
	require.Equal(t, int32(1), lookups.Load())
	rsp, err := p.ResolveDomains(context.Background(), &egresspb.ResolveDomainsReq{Domains: []string{"127.0.0.1"}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestPublicDialChecksAddressesOnceAndNeverDialsPrivateIPs(t *testing.T) {
	var lookups, calls int
	lookup := func(context.Context, string) ([]string, error) {
		lookups++
		return []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "203.0.113.9", "::1", "2001:db8::1", "8.8.8.8"}, nil
	}
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		calls++
		require.Equal(t, "tcp", network)
		require.Equal(t, "8.8.8.8:443", address)
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}
	conn, err := publicDialer(lookup, dial)(context.Background(), "tcp", "api.binance.com:443")
	require.NoError(t, err)
	_ = conn.Close()
	require.Equal(t, 1, lookups)
	require.Equal(t, 1, calls)
	_, err = publicDialer(lookup, dial)(context.Background(), "tcp", "api.binance.com:80")
	require.Error(t, err)
	require.Equal(t, 1, lookups)
	_, err = publicDialer(func(context.Context, string) ([]string, error) { return strings.Split("127.0.0.1,::1", ","), nil }, dial)(context.Background(), "tcp", "api.binance.com:443")
	require.Error(t, err)
	require.Equal(t, 1, calls)
	var attempts int
	_, err = publicDialer(func(context.Context, string) ([]string, error) {
		return strings.Fields(strings.Repeat("8.8.8.8 ", 30)), nil
	}, func(context.Context, string, string) (net.Conn, error) {
		attempts++
		return nil, errors.New("unreachable")
	})(context.Background(), "tcp", "api.binance.com:443")
	require.Error(t, err)
	require.Equal(t, 16, attempts)
}
