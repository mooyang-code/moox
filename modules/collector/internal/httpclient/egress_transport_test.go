package httpclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type egressInvoker func(context.Context, string, string, any, any) error

func (f egressInvoker) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

type directTransport func(*http.Request) (*http.Response, error)

func (f directTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestEgressTransportPreservesHTTPAndFiltersCredentials(t *testing.T) {
	for _, status := range []int{200, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			calls := 0
			gateway := egressInvoker(func(ctx context.Context, service, method string, request, response any) error {
				calls++
				require.Equal(t, egressService, service)
				require.Equal(t, "Do", method)
				req := request.(*egresspb.DoReq)
				require.Equal(t, "api.binance.com", req.Host)
				require.Equal(t, "/a%2Fb", req.Path)
				require.Equal(t, "symbol=BTC%2BUSDT&n=1", req.Query)
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, []byte{0, 1, 255}, req.Body)
				require.Equal(t, map[string]string{"Accept": "application/json", "Content-Type": "application/octet-stream"}, req.Headers)
				require.Positive(t, req.TimeoutMs)
				require.LessOrEqual(t, req.TimeoutMs, int32(2000))
				*response.(*egresspb.DoRsp) = egresspb.DoRsp{
					RetInfo: &commonpb.RetInfo{}, Status: int32(status), Body: []byte("decoded"),
					Headers: map[string]string{"Content-Type": "application/json", "Content-Encoding": "gzip", "Content-Length": "300", "Set-Cookie": "secret", "Location": "https://evil.example/", "X-Internal": "private"},
				}
				return nil
			})
			transport, err := NewEgressTransport(gateway, []string{"*.binance.com"}, directTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("proxy request reached direct transport")
				return nil, nil
			}))
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.binance.com/a%2Fb?symbol=BTC%2BUSDT&n=1", bytes.NewReader([]byte{0, 1, 255}))
			require.NoError(t, err)
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Content-Type", "application/octet-stream")
			for _, header := range []string{"Authorization", "Cookie", "X-Space-Id", "X-Moox-Key-Id", "Connection", "Accept-Encoding"} {
				req.Header.Set(header, "must-not-leave")
			}
			rsp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			defer rsp.Body.Close()
			require.Equal(t, status, rsp.StatusCode)
			raw, err := io.ReadAll(rsp.Body)
			require.NoError(t, err)
			require.Equal(t, "decoded", string(raw))
			require.Equal(t, int64(7), rsp.ContentLength)
			require.True(t, rsp.Uncompressed)
			require.Equal(t, http.Header{"Content-Type": {"application/json"}}, rsp.Header)
			require.Equal(t, 1, calls)
		})
	}
}

func TestEgressHTTPClientKeepsOtherDomainsDirectAndProxyErrorsClosed(t *testing.T) {
	directCalls, proxyCalls := 0, 0
	direct := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directCalls++
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer direct.Close()
	gateway := egressInvoker(func(_ context.Context, _, _ string, request, _ any) error {
		proxyCalls++
		require.Equal(t, "/", request.(*egresspb.DoReq).Path)
		return errors.New("proxy unavailable")
	})
	transport, err := NewEgressTransport(gateway, []string{"*.binance.com"}, direct.Client().Transport)
	require.NoError(t, err)
	client := &http.Client{Transport: transport}
	rsp, err := client.Get(direct.URL)
	require.NoError(t, err)
	_ = rsp.Body.Close()
	_, err = client.Get("https://api.binance.com")
	require.ErrorContains(t, err, "proxy unavailable")
	require.Equal(t, 1, proxyCalls)
	require.Equal(t, 1, directCalls)
	transport.CloseIdleConnections()
}

func TestEgressHTTPClientSnapshotIPsCannotBypassProxy(t *testing.T) {
	calls := 0
	client, err := NewEgressHTTPClient(egressInvoker(func(_ context.Context, _, _ string, req, rsp any) error {
		calls++
		require.Equal(t, "api.binance.com", req.(*egresspb.DoReq).Host)
		*rsp.(*egresspb.DoRsp) = egresspb.DoRsp{RetInfo: &commonpb.RetInfo{}, Status: 200, Body: []byte(`{"ok":true}`)}
		return nil
	}), []string{"*.binance.com"})
	require.NoError(t, err)
	defer client.Close()
	var result map[string]bool
	require.NoError(t, client.GetWithIPs(context.Background(), "api.binance.com", []string{"127.0.0.1", "1.1.1.1"}, "/api", nil, &result))
	require.True(t, result["ok"])
	require.Equal(t, 1, calls)
}

func TestEgressTransportRejectsInvalidRequestsBeforeRPC(t *testing.T) {
	transport, err := NewEgressTransport(egressInvoker(func(context.Context, string, string, any, any) error {
		t.Fatal("invalid request reached proxy")
		return nil
	}), []string{"*.binance.com"}, nil)
	require.NoError(t, err)
	for _, raw := range []string{"http://api.binance.com/", "https://api.binance.com:444/", "https://user:password@api.binance.com/"} {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		require.NoError(t, err)
		_, err = transport.RoundTrip(req)
		require.Error(t, err)
	}
	req, err := http.NewRequest(http.MethodDelete, "https://api.binance.com/", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.Error(t, err)
	req, err = http.NewRequest(http.MethodPost, "https://api.binance.com/", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.ErrorContains(t, err, "1 MiB")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.binance.com/", nil)
	require.NoError(t, err)
	_, err = transport.RoundTrip(req)
	require.ErrorIs(t, err, context.Canceled)
}

func TestEgressTransportCancellationUnblocksRequestBody(t *testing.T) {
	transport, err := NewEgressTransport(egressInvoker(func(context.Context, string, string, any, any) error {
		t.Fatal("canceled body reached proxy")
		return nil
	}), []string{"*.binance.com"}, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.binance.com/", reader)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := transport.RoundTrip(req); done <- err }()
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not unblock body read")
	}
}

func TestEgressHTTPClientPreservesRedirectResponse(t *testing.T) {
	client, err := NewEgressHTTPClient(egressInvoker(func(_ context.Context, _, _ string, _, response any) error {
		*response.(*egresspb.DoRsp) = egresspb.DoRsp{RetInfo: &commonpb.RetInfo{}, Status: 302, Headers: map[string]string{"Location": "https://must-not-connect.invalid/"}}
		return nil
	}), []string{"*.binance.com"})
	require.NoError(t, err)
	defer client.Close()
	rsp, err := client.httpClient.Get("https://api.binance.com/")
	require.NoError(t, err)
	defer rsp.Body.Close()
	require.Equal(t, 302, rsp.StatusCode)
	require.Empty(t, rsp.Header.Get("Location"))
}
func TestEgressTransportRejectsInvalidRPCResponses(t *testing.T) {
	for _, response := range []*egresspb.DoRsp{
		{}, {RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION}, Status: 200},
		{RetInfo: &commonpb.RetInfo{}, Status: 99}, {RetInfo: &commonpb.RetInfo{}, Status: 600},
	} {
		proxy, err := NewEgressTransport(egressInvoker(func(_ context.Context, _, _ string, _, output any) error {
			proto.Merge(output.(*egresspb.DoRsp), response)
			return nil
		}), []string{"*.binance.com"}, nil)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodGet, "https://api.binance.com/", nil)
		require.NoError(t, err)
		_, err = proxy.RoundTrip(req)
		require.Error(t, err)
	}
}
