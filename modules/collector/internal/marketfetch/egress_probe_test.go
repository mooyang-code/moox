package marketfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEgressProbeChecksBinanceKlineEndpoint(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/ping":
			_, _ = w.Write([]byte(`{}`))
		case "/api/v3/klines":
			_, _ = w.Write([]byte(`[[1700000000000,"1","2","0.5","1.5","3",1700000059999,"4",1,"2","3","0"]]`))
		case "/ip":
			_, _ = w.Write([]byte("198.51.100.1"))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	response, err := runBinanceEgressChecks(context.Background(), server.Client(), server.URL, server.URL+"/ip", "/api/v3/ping", "/api/v3/klines?symbol=BTCUSDT&interval=1m&limit=1", "binance", "spot")
	require.NoError(t, err)
	details := response.Data.(map[string]interface{})["details"].(map[string]string)
	require.Contains(t, details, "provider_kline")
}
