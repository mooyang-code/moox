package marketfetch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimerRequestFromEnv(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto_market")
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "binance")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "spot")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "bars")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "ETH-USDT|BTC-USDT")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"BTC-USDT":"BTCUSDT","ETH-USDT":"ETHUSDT"}`)
	t.Setenv("MOOX_MARKET_FETCH_ASSIGNMENT_HASH", "abc")
	t.Setenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON", `{"api.binance.com":["203.0.113.1"]}`)
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://10.0.0.1:11003")
	req, target, err := TimerRequestFromEnv("req", "node", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "ip://10.0.0.1:11003", target)
	require.Len(t, req.Items, 2)
	require.Equal(t, "BTCUSDT", req.Items[0].Symbol)
	require.Equal(t, "BTC-USDT", req.Items[0].SubjectID)
}

func TestTimerRequestFromEnvMalformedDNSFallsBackToPlatformResolver(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto_market")
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "binance")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "spot")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "bars")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "BTC-USDT")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"BTC-USDT":"BTCUSDT"}`)
	t.Setenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON", "not-json")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://10.0.0.1:11003")
	req, _, err := TimerRequestFromEnv("req", "node", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.NoError(t, err)
	require.Empty(t, req.DNSRoutes)
}
