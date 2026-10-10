package test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/collector/internal/dnsresolver"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// TestEgressDNSCollectorEnvironmentProductionE2E proves the deployed
// Egress -> Collector snapshot boundary and the exact environment payload that
// the existing CloudNode reconciler submits to SCF. It is opt-in because it
// requires the deployed Collector configuration and signing file.
func TestEgressDNSCollectorEnvironmentProductionE2E(t *testing.T) {
	if os.Getenv("MOOX_RUN_REAL_EGRESS_DNS_E2E") != "1" {
		t.Skip("set MOOX_RUN_REAL_EGRESS_DNS_E2E=1 to run against a deployed gateway")
	}
	configPath := strings.TrimSpace(os.Getenv("MOOX_COLLECTOR_APP_CONFIG"))
	require.NotEmpty(t, configPath, "provide the deployed Collector app.yaml path")
	cfg, err := bootstrap.Load(configPath)
	require.NoError(t, err)
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	defer gateway.Close()
	domains := appendUniqueDomains(appendUniqueDomains(nil, cfg.DNS.Domains...), cfg.EgressProxy.DNS.Domains...)
	remote := dnsresolver.NewEgressClient(gateway, 15*time.Second)
	coordinator := dnsresolver.NewCoordinator(dnsresolver.CoordinatorConfig{Remote: remote, RemoteDomains: cfg.EgressProxy.DNS.Domains, Domains: domains, Interval: time.Nanosecond})
	require.NoError(t, coordinator.Refresh(context.Background()))
	snapshot := coordinator.Snapshot()
	require.NotEmpty(t, snapshot)

	subject := "BTC-USDT"
	assignment := marketfetch.NodeAssignment{
		Provider: "binance", MarketType: "spot", DatasetID: "dataset_binance_kline_1m",
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
