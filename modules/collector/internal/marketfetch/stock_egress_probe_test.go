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
			_, _ = w.Write([]byte("198.51.100.8\n"))
		case "/kline":
			_, _ = w.Write([]byte(`[{"open":"10.00","close":"10.10"}]` + strings.Repeat("x", 800)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	response, err := stockEgressProbeWithClient(context.Background(), &http.Client{Timeout: time.Second}, server.URL+"/ip", server.URL+"/kline")
	require.NoError(t, err)
	data := response.Data.(map[string]interface{})
	details := data["details"].(map[string]string)
	assert.Equal(t, map[string]string{"public_ip": "198.51.100.8", "sina_kline": "ok"}, details)

	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	assert.Less(t, len(encoded), 512)
	assert.NotContains(t, string(encoded), strings.Repeat("x", 32))
}
