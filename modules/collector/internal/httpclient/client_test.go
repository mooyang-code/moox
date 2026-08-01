package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/jobcontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/log"
)

type httpLogCapture struct {
	info []string
	warn []string
}

func (l *httpLogCapture) Trace(...interface{})          {}
func (l *httpLogCapture) Tracef(string, ...interface{}) {}
func (l *httpLogCapture) Debug(...interface{})          {}
func (l *httpLogCapture) Debugf(string, ...interface{}) {}
func (l *httpLogCapture) Info(args ...interface{}) {
	l.info = append(l.info, fmt.Sprint(args...))
}
func (l *httpLogCapture) Infof(format string, args ...interface{}) {
	l.info = append(l.info, fmt.Sprintf(format, args...))
}
func (l *httpLogCapture) Warn(args ...interface{}) {
	l.warn = append(l.warn, fmt.Sprint(args...))
}
func (l *httpLogCapture) Warnf(format string, args ...interface{}) {
	l.warn = append(l.warn, fmt.Sprintf(format, args...))
}
func (l *httpLogCapture) Error(...interface{})          {}
func (l *httpLogCapture) Errorf(string, ...interface{}) {}
func (l *httpLogCapture) Fatal(...interface{})          {}
func (l *httpLogCapture) Fatalf(string, ...interface{}) {}
func (l *httpLogCapture) Sync() error                   { return nil }
func (l *httpLogCapture) SetLevel(string, log.Level)    {}
func (l *httpLogCapture) GetLevel(string) log.Level     { return log.LevelInfo }
func (l *httpLogCapture) With(...log.Field) log.Logger  { return l }

func TestHTTPClientSuccessLogIsInfoAndDoesNotLeakURLOrQuery(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	capture := &httpLogCapture{}
	original := log.GetDefaultLogger()
	log.SetLogger(capture)
	t.Cleanup(func() { log.SetLogger(original) })

	var result map[string]string
	ctx := jobcontext.WithJobItemID(context.Background(), "job-item-a")
	err = NewHTTPClient(server.Client()).GetWithIP(
		ctx,
		parsed.Host,
		"/api/v3/klines",
		url.Values{"api_key": {"super-secret"}, "symbol": {"BTCUSDT"}},
		&result,
		"",
	)
	require.NoError(t, err)
	require.Len(t, capture.info, 1)
	assert.Contains(t, capture.info[0], "collector_http_completed")
	assert.Contains(t, capture.info[0], "domain="+parsed.Host)
	assert.Contains(t, capture.info[0], "status=200")
	assert.Contains(t, capture.info[0], "duration_ms=")
	assert.Contains(t, capture.info[0], `job_item_id="job-item-a"`)
	assert.NotContains(t, capture.info[0], "super-secret")
	assert.NotContains(t, capture.info[0], "/api/v3/klines")
	assert.NotContains(t, capture.info[0], server.URL)
}

func TestHTTPClientTLSFailureDoesNotEmitSuccessLog(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	capture := &httpLogCapture{}
	original := log.GetDefaultLogger()
	log.SetLogger(capture)
	t.Cleanup(func() { log.SetLogger(original) })

	err = NewHTTPClient().GetWithIP(context.Background(), parsed.Host, "", nil, &map[string]string{}, "")
	require.Error(t, err)
	assert.Empty(t, capture.info)
}

func TestNewHTTPClient_ShouldInitializeClient(t *testing.T) {
	client := NewHTTPClient()
	require.NotNil(t, client)
	require.NotNil(t, client.httpClient)
	transport, ok := client.httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	assert.False(t, transport.TLSClientConfig.InsecureSkipVerify)
}

func TestHTTPClientRejectsUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	err = NewHTTPClient().Get(context.Background(), parsed.Host, "", nil, &map[string]string{})

	require.Error(t, err)
}

func TestHTTPClient_GetWithIP_UnreachableHost_ShouldReturnError(t *testing.T) {
	client := NewHTTPClient()
	var result map[string]string
	err := client.GetWithIP(context.Background(), "invalid.example.test", "/api/v3/ping", nil, &result, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "请求 invalid.example.test 失败")
}

func TestHTTPClient_Get_ShouldDelegateToGetWithIP(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	client := NewHTTPClient()
	client.httpClient = server.Client()

	var result map[string]string
	err = client.Get(context.Background(), parsed.Host, parsed.Path, nil, &result)
	require.NoError(t, err)
	assert.Equal(t, "ok", result["status"])
}

func TestHTTPClient_Get_ShouldDecodeJSONResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	client := NewHTTPClient()
	client.httpClient = server.Client()

	var result map[string]string
	err = client.GetWithIP(context.Background(), parsed.Host, parsed.Path, nil, &result, "")
	require.NoError(t, err)
	assert.Equal(t, "ok", result["status"])
}

func TestHTTPClient_GetWithIPStreamConsumesResponseBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"symbols":[{"symbol":"BTCUSDT"}]}`))
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	var got map[string]any
	err = NewHTTPClient(server.Client()).GetWithIPStream(
		context.Background(), parsed.Host, "/api/v3/exchangeInfo", nil, "",
		func(reader io.Reader) error { return json.NewDecoder(reader).Decode(&got) },
	)
	require.NoError(t, err)
	assert.Len(t, got["symbols"], 1)
}

func TestHTTPClient_Get_ShouldRejectNonOKStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	capture := &httpLogCapture{}
	original := log.GetDefaultLogger()
	log.SetLogger(capture)
	t.Cleanup(func() { log.SetLogger(original) })

	client := NewHTTPClient()
	client.httpClient = server.Client()
	var result map[string]string
	requestPath := "/api/v3/klines"
	err = client.GetWithIP(context.Background(), parsed.Host, requestPath, nil, &result, "")
	var statusErr *StatusError
	require.True(t, errors.As(err, &statusErr))
	assert.Equal(t, http.StatusBadGateway, statusErr.StatusCode)
	require.Len(t, capture.warn, 1)
	assert.Contains(t, capture.warn[0], "collector_http_failed")
	assert.Contains(t, capture.warn[0], "domain="+parsed.Host)
	assert.Contains(t, capture.warn[0], "status=502")
	assert.NotContains(t, capture.warn[0], requestPath)
	assert.NotContains(t, capture.warn[0], server.URL)
}

func TestParseBestIPs_ShouldSplitPlusSeparatedValues(t *testing.T) {
	assert.Equal(t, []string{"1.2.3.4", "5.6.7.8"}, parseBestIPs("1.2.3.4+5.6.7.8"))
	assert.Nil(t, parseBestIPs(""))
}

func TestConvertMapToSlice_ShouldCollectKeys(t *testing.T) {
	m := &sync.Map{}
	m.Store("1.1.1.1", struct{}{})
	m.Store("2.2.2.2", struct{}{})
	assert.ElementsMatch(t, []string{"1.1.1.1", "2.2.2.2"}, convertMapToSlice(m))
}

func TestCreateResolver_ShouldReturnResolver(t *testing.T) {
	assert.NotNil(t, createResolver("localhost", time.Second))
	assert.NotNil(t, createResolver("8.8.8.8", time.Second))
}

func TestParseServerResponse_ShouldValidateRetInfo(t *testing.T) {
	records, err := parseServerResponse([]byte(`{"ret_info":{"code":0,"msg":"ok"},"records":[{"domain":"a.com","best_ips":"1.1.1.1","success":true}]}`))
	assert.NoError(t, err)
	assert.Len(t, records, 1)

	_, err = parseServerResponse([]byte(`{"ret_info":{"code":1,"msg":"bad"}}`))
	assert.Error(t, err)
}

func TestParseBestIPs_ValidString_ShouldSplitIPs(t *testing.T) {
	ips := parseBestIPs("1.2.3.4+5.6.7.8+ 9.10.11.12 ")
	assert.Equal(t, []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"}, ips)
}

func TestParseBestIPs_EmptyString_ShouldReturnNil(t *testing.T) {
	assert.Nil(t, parseBestIPs(""))
}

func TestParseServerResponse_SuccessCode_ShouldReturnRecords(t *testing.T) {
	raw := []byte(`{"ret_info":{"code":0,"msg":"ok"},"records":[{"domain":"api.binance.com","best_ips":"1.2.3.4","resolve_at":"2026-01-01T00:00:00Z","success":true}]}`)
	records, err := parseServerResponse(raw)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "api.binance.com", records[0].Domain)
}

func TestParseServerResponse_ErrorCode_ShouldReturnError(t *testing.T) {
	raw := []byte(`{"ret_info":{"code":1,"msg":"failed"},"records":[]}`)
	_, err := parseServerResponse(raw)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed")
}

func TestGetBestIP_WithAvailableRecord_ShouldReturnFirstAvailable(t *testing.T) {
	Init()
	updateDNSRecords([]*DNSRecord{{
		Domain: "api.binance.com",
		IPList: []*IPInfo{
			{IP: "1.1.1.1", Available: false},
			{IP: "2.2.2.2", Available: true},
		},
	}})
	assert.Equal(t, "2.2.2.2", GetBestIP("api.binance.com"))
}

func TestGetNextAvailableIP_WithExcludeList_ShouldSkipExcluded(t *testing.T) {
	Init()
	updateDNSRecords([]*DNSRecord{{
		Domain: "api.binance.com",
		IPList: []*IPInfo{
			{IP: "1.1.1.1", Available: true},
			{IP: "2.2.2.2", Available: true},
		},
	}})
	assert.Equal(t, "2.2.2.2", GetNextAvailableIP("api.binance.com", []string{"1.1.1.1"}))
}

func TestGetAllDNSRecords_ShouldReturnCopy(t *testing.T) {
	Init()
	updateDNSRecords([]*DNSRecord{{Domain: "api.binance.com", IPList: []*IPInfo{{IP: "1.1.1.1", Available: true}}}})
	all := GetAllDNSRecords()
	require.Contains(t, all, "api.binance.com")
	all["api.binance.com"] = nil
	assert.NotNil(t, GetAllDNSRecords()["api.binance.com"])
}

func TestProbeTCP_Localhost_ShouldSucceed(t *testing.T) {
	latency, available := probeTCP(t.Context(), "127.0.0.1", 1, 500*time.Millisecond)
	assert.False(t, available)
	assert.Equal(t, int64(0), latency)
}
