package marketfetch

import (
	"fmt"
	"strings"
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
	require.Equal(t, ManagedDNSHash(map[string]sources.DNSResolution{
		"API.BINANCE.COM.": {IPs: []string{"203.0.113.2", "203.0.113.1"}, ResolvedAt: time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)},
	}), env["MOOX_MARKET_FETCH_DNS_HASH"])
	require.Equal(t, "2026-08-04T00:00:00Z", env["MOOX_MARKET_FETCH_DNS_UPDATED_AT"])
}

func TestManagedDNSHashIgnoresLatencyOrderedIPChanges(t *testing.T) {
	resolvedAt := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	first := map[string]sources.DNSResolution{
		"api.binance.com": {
			IPs:        []string{"203.0.113.2", "203.0.113.1"},
			LatencyMS:  map[string]uint32{"203.0.113.2": 10, "203.0.113.1": 20},
			ResolvedAt: resolvedAt,
		},
	}
	second := map[string]sources.DNSResolution{
		"api.binance.com": {
			IPs:        []string{"203.0.113.1", "203.0.113.2"},
			LatencyMS:  map[string]uint32{"203.0.113.1": 8, "203.0.113.2": 25},
			ResolvedAt: resolvedAt.Add(time.Minute),
		},
	}

	firstRoutes, firstHash, _ := normalizeDNSRoutes(first)
	secondRoutes, secondHash, _ := normalizeDNSRoutes(second)
	require.Equal(t, firstRoutes, secondRoutes)
	require.Equal(t, firstHash, secondHash)
	require.Equal(t, []string{"203.0.113.1", "203.0.113.2"}, firstRoutes["api.binance.com"])
}

func TestBuildManagedEnvironmentFitsTypicalThirtySymbols(t *testing.T) {
	subjects := make([]string, 0, 30)
	externals := make(map[string]string, 30)
	for index := 0; index < 30; index++ {
		subject := fmt.Sprintf("COIN%d-USDT", index)
		subjects = append(subjects, subject)
		externals[subject] = fmt.Sprintf("COIN%dUSDT", index)
	}
	_, err := BuildManagedEnvironment(NodeAssignment{
		Provider: "binance", MarketType: "spot", DatasetID: "binance_spot_kline_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals, Enabled: true,
	}, map[string]sources.DNSResolution{"data-api.binance.vision": {IPs: []string{"203.0.113.1", "203.0.113.2"}}})
	require.NoError(t, err)
}

func TestBuildManagedEnvironmentRejectsOversizedManagedAssignment(t *testing.T) {
	subjects := make([]string, 30)
	externals := make(map[string]string, len(subjects))
	for index := range subjects {
		subjects[index] = fmt.Sprintf("%030d", index)
		externals[subjects[index]] = strings.Repeat("X", 32)
	}
	_, err := BuildManagedEnvironment(NodeAssignment{
		Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals,
		AssignmentHash: "hash",
	}, nil)
	require.ErrorContains(t, err, "reduce symbols or split")
}
