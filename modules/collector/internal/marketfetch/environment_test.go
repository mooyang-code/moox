package marketfetch

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/stretchr/testify/require"
)

func TestBuildManagedEnvironmentCanonicalizesDNS(t *testing.T) {
	assignment := NodeAssignment{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"ETH-USDT", "BTC-USDT"}, ExternalSymbols: map[string]string{"ETH-USDT": "ETHUSDT", "BTC-USDT": "BTCUSDT"}, Enabled: true, AssignmentHash: "abc"}
	env, err := BuildManagedEnvironment(assignment, map[string]sources.DNSResolution{
		"API.BINANCE.COM.": {IPs: []string{"203.0.113.2", "203.0.113.1"}, ResolvedAt: time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)},
	})
	require.NoError(t, err)
	require.Equal(t, "BTC-USDT|ETH-USDT", env["MOOX_MARKET_FETCH_SUBJECTS"])
	require.Equal(t, `{"api.binance.com":["203.0.113.1","203.0.113.2"]}`, env["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"])
	require.Equal(t, "2026-08-04T00:00:00Z", env["MOOX_MARKET_FETCH_DNS_UPDATED_AT"])
}
