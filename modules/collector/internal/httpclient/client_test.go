package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestHTTPClient_Get_ShouldRejectNonOKStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	client := NewHTTPClient()
	client.httpClient = server.Client()
	var result map[string]string
	err = client.GetWithIP(context.Background(), parsed.Host, parsed.Path, nil, &result, "")
	var statusErr *StatusError
	require.True(t, errors.As(err, &statusErr))
	assert.Equal(t, http.StatusBadGateway, statusErr.StatusCode)
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
