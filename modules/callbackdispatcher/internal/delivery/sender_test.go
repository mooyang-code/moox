package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/require"
)

var testConfig = Config{ConnectTimeout: time.Second, RequestTimeout: 500 * time.Millisecond}

// testSender 让地址检查照常执行，但所有拨号都落到测试服务器上：域名解析成固定的公网地址。
func testSender(t *testing.T, server *httptest.Server, resolved ...string) (*Sender, *atomic.Int32) {
	t.Helper()
	if len(resolved) == 0 {
		resolved = []string{"93.184.216.34"}
	}
	dials := &atomic.Int32{}
	return newSender(testConfig,
		func(context.Context, string) ([]string, error) { return resolved, nil },
		func(ctx context.Context, network, _ string) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		}), dials
}

func sampleRequest() Request {
	return Request{
		RuleID: "rule-1", EventID: "event-1", EventName: "event.collector.instrument.delist_scheduled", EventVersion: 1,
		SpaceID: "mooxsys", SubjectID: "binance:SPOT:FOOUSDT", OccurredAt: time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC),
		PayloadJSON: `{"exchange":"binance","exchange_symbol":"FOOUSDT"}`,
	}
}

func TestSendFormatAndSignatureVerifiableByExampleCode(t *testing.T) {
	var captured struct {
		header http.Header
		body   []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.header = r.Header.Clone()
		captured.body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	sender, _ := testSender(t, server)

	result := sender.Send(context.Background(), Target{URL: "http://hook.example.com/notify", Secret: "whsec_test"}, sampleRequest())
	require.Equal(t, Success, result.Class, result.Err)
	require.Equal(t, 200, result.StatusCode)
	require.Equal(t, "ok", result.Snippet)

	require.Equal(t, "application/json", captured.header.Get("Content-Type"))
	require.Equal(t, "event-1", captured.header.Get("X-MooX-Event-Id"))
	require.Equal(t, "event.collector.instrument.delist_scheduled", captured.header.Get("X-MooX-Event"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(captured.body, &body))
	require.Equal(t, "event-1", body["event_id"])
	require.Equal(t, "rule-1", body["rule_id"])
	require.Equal(t, "2026-11-04T08:00:00Z", body["occurred_at"])
	require.Equal(t, "FOOUSDT", body["data"].(map[string]any)["exchange_symbol"])

	// 回调方按文档里的几行示例代码验签：HMAC-SHA256(密钥, 时间戳 + "." + 请求体)。
	timestamp := captured.header.Get("X-MooX-Timestamp")
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write([]byte(timestamp + "." + string(captured.body)))
	require.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), captured.header.Get("X-MooX-Signature"))
	require.Equal(t, notification.WebhookSignature("whsec_test", timestamp, captured.body), captured.header.Get("X-MooX-Signature"))
	require.NotEqual(t, notification.WebhookSignature("other", timestamp, captured.body), captured.header.Get("X-MooX-Signature"))
	issued, err := strconv.ParseInt(timestamp, 10, 64)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), time.UnixMilli(issued), 10*time.Second, "时间戳是毫秒")
}

func TestStatusClassification(t *testing.T) {
	for status, want := range map[int]Class{
		200: Success, 204: Success, 299: Success,
		400: Permanent, 401: Permanent, 403: Permanent, 404: Permanent, 410: Permanent,
		408: Retryable, 429: Retryable, 500: Retryable, 502: Retryable, 503: Retryable,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		sender, _ := testSender(t, server)
		result := sender.Send(context.Background(), Target{URL: "http://hook.example.com/", Secret: "s"}, sampleRequest())
		server.Close()
		require.Equal(t, want, result.Class, "状态码 %d", status)
		require.Equal(t, status, result.StatusCode)
		if want != Success {
			require.Error(t, result.Err)
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var followed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			followed.Store(true)
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer server.Close()
	sender, _ := testSender(t, server)
	result := sender.Send(context.Background(), Target{URL: "http://hook.example.com/start", Secret: "s"}, sampleRequest())
	require.Equal(t, Permanent, result.Class)
	require.Equal(t, 302, result.StatusCode)
	require.False(t, followed.Load())
}

func TestOversizedResponseIsBoundedAndSnippetKept(t *testing.T) {
	var served atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := []byte(strings.Repeat("x", 4096))
		for i := 0; i < 1024; i++ { // 4MB
			n, err := w.Write(chunk)
			served.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()
	sender, _ := testSender(t, server)
	result := sender.Send(context.Background(), Target{URL: "http://hook.example.com/", Secret: "s"}, sampleRequest())
	require.Equal(t, Success, result.Class)
	require.Len(t, result.Snippet, 1024)
}

func TestAddressGuard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	sender, dials := testSender(t, server)
	for _, url := range []string{
		"http://127.0.0.1/hook", "http://localhost/hook", "http://[::1]/hook", "http://169.254.169.254/latest/meta-data/",
		"http://[fe80::1]/hook", "http://224.0.0.1/hook", "http://10.0.0.5/hook", "http://192.168.1.10/hook", "http://100.64.0.1/hook", "http://[fd00::1]/hook",
	} {
		resolved := []string{}
		if strings.Contains(url, "localhost") {
			resolved = []string{"127.0.0.1", "::1"}
		}
		s := sender
		if len(resolved) != 0 {
			s, _ = testSender(t, server, resolved...)
		}
		result := s.Send(context.Background(), Target{URL: url, Secret: "s"}, sampleRequest())
		require.Equal(t, Permanent, result.Class, url)
		require.Zero(t, result.StatusCode, url)
	}
	require.Zero(t, dials.Load(), "被拒绝的地址不能拨号")

	// 域名解析到回环（DNS 重绑定）同样拒绝。
	rebinding, rebindDials := testSender(t, server, "127.0.0.1")
	result := rebinding.Send(context.Background(), Target{URL: "http://rebind.example.com/hook", Secret: "s"}, sampleRequest())
	require.Equal(t, Permanent, result.Class)
	require.Zero(t, rebindDials.Load())

	// 勾选“允许内网地址”后内网放行，回环与元数据地址依旧拒绝。
	allowed := Target{URL: "http://10.0.0.5/hook", Secret: "s", AllowPrivate: true}
	require.Equal(t, Success, sender.Send(context.Background(), allowed, sampleRequest()).Class)
	for _, url := range []string{"http://127.0.0.1/hook", "http://169.254.169.254/", "http://[::1]/hook"} {
		require.Equal(t, Permanent, sender.Send(context.Background(), Target{URL: url, Secret: "s", AllowPrivate: true}, sampleRequest()).Class, url)
	}
}

func TestOnlyHTTPAndHTTPSAreAccepted(t *testing.T) {
	for _, raw := range []string{"ftp://example.com/x", "file:///etc/passwd", "gopher://example.com", "example.com/hook", "http://", "http://user:pass@example.com/", strings.Repeat("http://a.com/", 400)} {
		require.Error(t, ValidateURL(raw), raw)
	}
	for _, raw := range []string{"http://example.com/hook", "https://example.com:8443/hook?x=1"} {
		require.NoError(t, ValidateURL(raw), raw)
	}
}

func TestEnvironmentProxyIsIgnored(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	sender, dials := testSender(t, server)
	result := sender.Send(context.Background(), Target{URL: "http://hook.example.com/", Secret: "s"}, sampleRequest())
	require.Equal(t, Success, result.Class, result.Err)
	require.EqualValues(t, 1, dials.Load())
}

func TestTimeoutAndConnectionFailureAreRetryable(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer slow.Close()
	sender, _ := testSender(t, slow)
	started := time.Now()
	result := sender.Send(context.Background(), Target{URL: "http://hook.example.com/", Secret: "s"}, sampleRequest())
	require.Equal(t, Retryable, result.Class)
	require.Contains(t, result.Err.Error(), "超时")
	require.Less(t, time.Since(started), 2*time.Second)

	refused := newSender(testConfig,
		func(context.Context, string) ([]string, error) { return []string{"93.184.216.34"}, nil },
		func(context.Context, string, string) (net.Conn, error) { return nil, io.ErrClosedPipe })
	result = refused.Send(context.Background(), Target{URL: "http://hook.example.com/", Secret: "s"}, sampleRequest())
	require.Equal(t, Retryable, result.Class)
}

func TestRetrySchedule(t *testing.T) {
	want := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour}
	for i, delay := range want {
		got, ok := NextDelay(i + 1)
		require.True(t, ok)
		require.Equal(t, delay, got)
	}
	_, ok := NextDelay(len(want) + 1)
	require.False(t, ok)
	_, ok = NextDelay(0)
	require.False(t, ok)
}
