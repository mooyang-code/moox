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
	assignment := NodeAssignment{Provider: "eastmoney", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity", SourceID: "stockcn_http", SeriesTag: "cn-equity", DatasetID: "bars", Frequency: "1m", Subjects: []string{"ETH-USDT", "BTC-USDT"}, ExternalSymbols: map[string]string{"ETH-USDT": "ETHUSDT", "BTC-USDT": "BTCUSDT"}, Enabled: true, AssignmentHash: "abc"}
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
	require.Equal(t, "stockcn", env["MOOX_MARKET_FETCH_MARKET_ID"])
	require.Equal(t, "equity", env["MOOX_MARKET_FETCH_INSTRUMENT_TYPE"])
	require.Equal(t, "stockcn_http", env["MOOX_MARKET_FETCH_SOURCE_ID"])
	require.Equal(t, "cn-equity", env["MOOX_MARKET_FETCH_SERIES_TAG"])
}

func TestTimerRequestFromEnvCarriesBoundSourceIDToEveryItem(t *testing.T) {
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "tencent")
	t.Setenv("MOOX_MARKET_FETCH_SOURCE_ID", "stockcn_http")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", StockCNDatasetID)
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_SPACE_ID", StockCNSpaceID)
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "600000.XSHG|000001.XSHE")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", "")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "1")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "0")

	request, _, err := TimerRequestFromEnv("request-1", "function-1", time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "stockcn_http", request.SourceID)
	require.Len(t, request.Items, 2)
	for _, item := range request.Items {
		require.Equal(t, "tencent", item.Provider)
		require.Equal(t, "stockcn_http", item.SourceID)
	}
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
		Provider: "binance", MarketType: "spot", DatasetID: "dataset_binance_spot_kline_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals, Enabled: true,
	}, map[string]sources.DNSResolution{"data-api.binance.vision": {IPs: []string{"203.0.113.1", "203.0.113.2"}}})
	require.NoError(t, err)
}

func TestBuildManagedEnvironmentAllowsStockGroupAboveThirty(t *testing.T) {
	subjects := make([]string, 0, 40)
	externals := make(map[string]string, 40)
	for index := 0; index < 40; index++ {
		subject := fmt.Sprintf("%06d.XSHG", 600000+index)
		subjects = append(subjects, subject)
		externals[subject] = fmt.Sprintf("sh%06d", index)
	}
	_, err := buildManagedEnvironment(NodeAssignment{
		Provider: "sina", RouteProvider: "stockcn_multi", MarketType: "equity", DatasetID: StockCNDatasetID,
		Frequency: "1m", Subjects: subjects, ExternalSymbols: externals, RouteVersion: StockCNRouteID, Enabled: true,
	}, nil, stockCNMaxManagedEnvironmentSize)
	require.NoError(t, err)
}

func TestBuildManagedEnvironmentUsesCompactStockSymbolOverrides(t *testing.T) {
	env, err := buildManagedEnvironment(NodeAssignment{
		Provider: "sina", RouteProvider: "stockcn_multi", MarketType: "equity", DatasetID: StockCNDatasetID,
		Frequency: "1m", Subjects: []string{"000001.XSHE", "600000.XSHG"},
		ExternalSymbols: map[string]string{"600000.XSHG": "custom-sh600000"}, RouteVersion: StockCNRouteID, Enabled: true,
	}, nil, stockCNMaxManagedEnvironmentSize)
	require.NoError(t, err)
	require.Equal(t, `{"600000.XSHG":"custom-sh600000"}`, env["MOOX_MARKET_FETCH_SYMBOLS_JSON"])
}

func TestBuildManagedEnvironmentDoesNotRequireStockSymbolMapping(t *testing.T) {
	_, err := buildManagedEnvironment(NodeAssignment{
		Provider: "sina", RouteProvider: "stockcn_multi", MarketType: "equity", DatasetID: StockCNDatasetID,
		Frequency: "1m", Subjects: []string{"000001.XSHE", "600000.XSHG"}, RouteVersion: StockCNRouteID, Enabled: true,
	}, nil, stockCNMaxManagedEnvironmentSize)
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

func TestBuildManagedEnvironmentDoesNotLeakHostMetricsSettings(t *testing.T) {
	t.Setenv("MOOX_METRICS_EVENTBUS_URL", "tls://127.0.0.1:4222")
	t.Setenv("MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE", "/Users/host/.config/moox/metrics.yaml")
	t.Setenv("MOOX_SCF_METRICS_EVENTBUS_URL", "")
	t.Setenv("MOOX_SCF_METRICS_EVENTBUS_CREDENTIAL_FILE", "")
	env, err := BuildManagedEnvironment(NodeAssignment{Provider: "sina", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m", Subjects: []string{"600000.XSHG"}, RouteVersion: StockCNRouteID}, nil)
	require.NoError(t, err)
	_, hasURL := env["MOOX_METRICS_EVENTBUS_URL"]
	_, hasCredentials := env["MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE"]
	require.False(t, hasURL)
	require.False(t, hasCredentials)
}

func TestBuildManagedEnvironmentUsesExplicitSCFMetricsSettings(t *testing.T) {
	t.Setenv("MOOX_METRICS_EVENTBUS_URL", "tls://127.0.0.1:4222")
	t.Setenv("MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE", "/Users/host/.config/moox/metrics.yaml")
	t.Setenv("MOOX_SCF_METRICS_EVENTBUS_URL", "tls://metrics.example:4222")
	t.Setenv("MOOX_SCF_METRICS_EVENTBUS_CREDENTIAL_FILE", "/var/task/metrics-publisher.yaml")
	env, err := BuildManagedEnvironment(NodeAssignment{Provider: "sina", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m", Subjects: []string{"600000.XSHG"}, RouteVersion: StockCNRouteID}, nil)
	require.NoError(t, err)
	require.Equal(t, "tls://metrics.example:4222", env["MOOX_METRICS_EVENTBUS_URL"])
	require.Equal(t, "/var/task/metrics-publisher.yaml", env["MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE"])
}
