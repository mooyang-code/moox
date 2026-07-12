package httpclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestGetAvailableIPs_ShouldReturnOnlyAvailable(t *testing.T) {
	Init()
	now := time.Now()
	updateDNSRecords([]*DNSRecord{{
		Domain:    "api.binance.com",
		ResolveAt:   now,
		IPList:      []*IPInfo{{IP: "1.1.1.1", Available: true}, {IP: "2.2.2.2", Available: false}},
		Success:     true,
	}})
	assert.Equal(t, []string{"1.1.1.1"}, GetAvailableIPs("api.binance.com"))
}

func TestGetDNSRecord_MissingDomain_ShouldReturnNil(t *testing.T) {
	Init()
	assert.Nil(t, GetDNSRecord("missing.example"))
}

func TestGetAllDNSRecords_ShouldReturnCopy(t *testing.T) {
	Init()
	updateDNSRecords([]*DNSRecord{{Domain: "api.binance.com", IPList: []*IPInfo{{IP: "1.1.1.1", Available: true}}}})
	all := GetAllDNSRecords()
	require.Contains(t, all, "api.binance.com")
	all["api.binance.com"] = nil
	assert.NotNil(t, GetDNSRecord("api.binance.com"))
}

func TestProbeTCP_Localhost_ShouldSucceed(t *testing.T) {
	latency, available := probeTCP(t.Context(), "127.0.0.1", 1, 500*time.Millisecond)
	assert.False(t, available)
	assert.Equal(t, int64(0), latency)
}
