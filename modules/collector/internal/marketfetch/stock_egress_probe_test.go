package marketfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStockEgressProbeReturnsCompactGateEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ip":
			_, _ = w.Write([]byte("8.8.8.8\n"))
		case "/kline":
			_, _ = w.Write([]byte(`var moox_probe=([{"day":"2026-08-28 14:59:00","open":"10.00","high":"10.20","low":"9.90","close":"10.10","volume":"100","amount":"1010"}]);` + strings.Repeat(" ", 800)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	response, err := stockEgressProbeWithClient(context.Background(), &http.Client{Timeout: time.Second}, server.URL+"/ip", server.URL+"/kline")
	require.NoError(t, err)
	data := response.Data.(map[string]interface{})
	details := data["details"].(map[string]string)
	assert.Equal(t, map[string]string{"public_ip": "8.8.8.8", "sina_kline": "ok"}, details)

	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	assert.Less(t, len(encoded), 512)
	assert.NotContains(t, string(encoded), strings.Repeat("x", 32))
}

func TestStockEgressIdentityProbeUsesReflectorFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ip" {
			_, _ = w.Write([]byte("8.8.8.8"))
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	response, err := stockEgressIdentityProbeWithClient(context.Background(), &http.Client{Timeout: time.Second}, server.URL+"/down", server.URL+"/ip")
	require.NoError(t, err)
	assert.True(t, response.Success)
	data := response.Data.(map[string]interface{})
	details := data["details"].(map[string]string)
	assert.Equal(t, "8.8.8.8", details["public_ip"])
}

func TestStockEgressProbeRejectsNonPublicAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.8", "100.64.0.1", "169.254.1.1", "198.51.100.8", "203.0.113.9", "::1", "fc00::1", "fc00::1%eth0", "2001:db8::1", "100::1", "100:0:0:1::1", "64:ff9b::1", "64:ff9b:1::1", "2001:2::1", "2001:10::1", "2001:20::1", "2002::1", "3fff::1", "5f00::1"} {
		t.Run(address, func(t *testing.T) {
			require.Error(t, validatePublicIPAddress([]byte(address)))
		})
	}
	require.NoError(t, validatePublicIPAddress([]byte("8.8.8.8")))
	require.NoError(t, validatePublicIPAddress([]byte("2606:4700:4700::1111")))
}

func TestStockEgressProbeRejectsMarkerOnlyMarketPayload(t *testing.T) {
	require.Error(t, validateSinaKline([]byte(`{"open":"not-a-price"}`)))
	require.Error(t, validateTencentKline([]byte(`{"m1":"marker-only"}`)))
	require.Error(t, validateEastMoneyKline([]byte(`{"trends":[]}`)))
}

func TestStockEgressProbeRejectsStructurallyInvalidQuotes(t *testing.T) {
	validators := []struct {
		name string
		fn   stockEgressValidator
		wrap func(string, string, string, string, string, string) []byte
	}{
		{name: "sina", fn: validateSinaKline, wrap: func(ts, open, high, low, close, volume string) []byte {
			return []byte(`var moox_probe=([{"day":"` + ts + `","open":"` + open + `","high":"` + high + `","low":"` + low + `","close":"` + close + `","volume":"` + volume + `","amount":"100"}]);`)
		}},
		{name: "tencent", fn: validateTencentKline, wrap: func(ts, open, high, low, close, volume string) []byte {
			return []byte(`m1_today={"data":{"sh600000":{"m1":[["` + ts + `","` + open + `","` + close + `","` + high + `","` + low + `","` + volume + `"]]}}};`)
		}},
		{name: "eastmoney", fn: validateEastMoneyKline, wrap: func(ts, open, high, low, close, volume string) []byte {
			return []byte(`{"data":{"trends":["` + ts + `,` + open + `,` + close + `,` + high + `,` + low + `,` + volume + `,100"]}}`)
		}},
	}
	for _, validator := range validators {
		t.Run(validator.name, func(t *testing.T) {
			require.Error(t, validator.fn(validator.wrap("marker", "10", "11", "9", "10", "100")))
			require.Error(t, validator.fn(validator.wrap("2026-08-28 14:59", "NaN", "NaN", "NaN", "NaN", "NaN")))
			require.Error(t, validator.fn(validator.wrap("2026-08-28 14:59", "10", "9", "11", "10", "100")))
		})
	}
}

func TestStockEgressProbeParsesProviderQuoteShapes(t *testing.T) {
	require.NoError(t, validateSinaKline([]byte(`/*<script>location.href='//sina.com';</script>*/
var moox_probe=([{"day":"2026-08-28 14:59:00","open":"10.00","high":"10.20","low":"9.90","close":"10.10","volume":"100","amount":"1010"}]);`)))
	require.NoError(t, validateTencentKline([]byte(`m1_today={"data":{"sh600000":{"m1":[["202608281459","10.00","10.10","10.20","9.90","100"]]}}};`)))
	require.NoError(t, validateEastMoneyKline([]byte(`{"data":{"trends":["2026-08-28 14:59,10.00,10.10,10.20,9.90,100,1010"]}}`)))
}
