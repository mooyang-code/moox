package marketfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
