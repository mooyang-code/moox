package test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/dnsresolver"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
)

// TestTradeDNSCollectorEnvironmentProductionE2E proves the deployed
// Trade -> Collector snapshot boundary and the exact environment payload that
// the existing CloudNode reconciler submits to SCF. It is opt-in because it
// requires the production Gateway caller credential.
func TestTradeDNSCollectorEnvironmentProductionE2E(t *testing.T) {
	if os.Getenv("MOOX_RUN_REAL_TRADE_DNS_E2E") != "1" {
		t.Skip("set MOOX_RUN_REAL_TRADE_DNS_E2E=1 to run against a deployed Trade Gateway")
	}
	target := strings.TrimSpace(os.Getenv("MOOX_TRADE_DNS_TARGET"))
	if target == "" {
		target = "ip://43.132.204.177:11003"
	}
	nodeID := strings.TrimSpace(os.Getenv("MOOX_TRADE_DNS_NODE_ID"))
	if nodeID == "" {
		nodeID = "compute-1"
	}
	// The live Collector hashes the union of the legacy local-DNS list and
	// the Trade resolver list. Keep the opt-in test's expected snapshot aligned
	// with both config blocks instead of silently selecting one of them.
	domains := appendUniqueDomains(nil, splitEnvList(os.Getenv("MOOX_TRADE_DNS_DOMAINS"))...)
	domains = appendUniqueDomains(domains, splitEnvList(os.Getenv("MOOX_COLLECTOR_DNS_DOMAINS"))...)
	domains = appendUniqueDomains(domains, splitEnvList(os.Getenv("MOOX_COLLECTOR_DNS_RESOLVER_DOMAINS"))...)
	if len(domains) == 0 {
		// Match production Collector app.yaml. The live coordinator hashes
		// the legacy DNS and Trade resolver domain sets together.
		domains = []string{"data-api.binance.vision", "api.binance.com", "fapi.binance.com"}
	}
	credentials := gatewayauth.CredentialsFromEnv()
	require.NotEmpty(t, credentials.KeyID)
	require.NotEmpty(t, credentials.Caller)
	require.NotEmpty(t, credentials.Secret)

	remote := dnsresolver.NewTradeClient(target, nodeID, credentials, 15*time.Second)
	coordinator := dnsresolver.NewCoordinator(nil, remote, domains, time.Nanosecond)
	require.NoError(t, coordinator.Refresh(context.Background()))
	snapshot := coordinator.Snapshot()
	require.NotEmpty(t, snapshot)

	subject := "BTC-USDT"
	assignment := marketfetch.NodeAssignment{
		Provider: "binance", MarketType: "spot", DatasetID: "binance_spot_kline_1m",
		Frequency: "1m", Subjects: []string{subject},
		ExternalSymbols: map[string]string{subject: "BTCUSDT"}, Enabled: true,
	}
	environment, err := marketfetch.BuildManagedEnvironment(assignment, snapshot)
	require.NoError(t, err)
	var routes map[string][]string
	require.NoError(t, json.Unmarshal([]byte(environment["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"]), &routes))
	for _, domain := range domains {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		require.NotEmpty(t, routes[host], "CloudNode payload has no route for %s", host)
	}
	require.NotEmpty(t, environment["MOOX_MARKET_FETCH_DNS_HASH"])
	require.NotEmpty(t, environment["MOOX_MARKET_FETCH_DNS_UPDATED_AT"])
	if path := strings.TrimSpace(os.Getenv("MOOX_EXPECTED_DNS_HASH_FILE")); path != "" {
		hash := environment["MOOX_MARKET_FETCH_DNS_HASH"]
		require.Regexp(t, `^[0-9a-f]{16}$`, hash)
		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("%s %d\n", hash, len(domains))), 0o600))
	}
}

func splitEnvList(raw string) []string {
	var result []string
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '|' || r == ' ' || r == '\n' || r == '\t' }) {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func appendUniqueDomains(dst []string, values ...string) []string {
	seen := make(map[string]struct{}, len(dst)+len(values))
	for _, value := range dst {
		seen[normalizeDomain(value)] = struct{}{}
	}
	for _, value := range values {
		key := normalizeDomain(value)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		dst = append(dst, value)
	}
	return dst
}

func normalizeDomain(value string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
}
