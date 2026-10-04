package test

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
)

// TestResolveDomainsProductionE2E is opt-in because it talks to the deployed
// Trade Gateway and depends on the operator's current credentials. It proves
// the public contract without printing the credential material.
func TestResolveDomainsProductionE2E(t *testing.T) {
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
	domains := splitEnvList(os.Getenv("MOOX_TRADE_DNS_DOMAINS"))
	if len(domains) == 0 {
		domains = []string{"fapi.binance.com"}
	}
	credentials := gatewayauth.CredentialsFromEnv()
	require.NotEmpty(t, credentials.KeyID, "MOOX_GATEWAY_SERVICE_KEY_ID is required")
	require.NotEmpty(t, credentials.Caller, "MOOX_GATEWAY_CALLER is required")
	require.NotEmpty(t, credentials.Secret, "MOOX_GATEWAY_SERVICE_SECRET_KEY is required")

	client := tradepb.NewTradeDNSResolverServiceClientProxy(gatewayauth.NewTRPCClientOptions(target, nodeID, credentials)...)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rsp, err := client.ResolveDomains(ctx, &tradepb.ResolveDomainsReq{Domains: domains, MaxIpsPerDomain: 4})
	require.NoError(t, err)
	require.NotNil(t, rsp)
	require.NotNil(t, rsp.GetRetInfo())
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.NotEmpty(t, rsp.GetResolutions(), "the deployed resolver returned no reachable domain")

	requested := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		requested[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))] = struct{}{}
	}
	require.Empty(t, rsp.GetUnresolvedDomains())
	require.Len(t, rsp.GetResolutions(), len(requested))
	resolved := make(map[string]bool, len(rsp.GetResolutions()))
	for _, item := range rsp.GetResolutions() {
		require.NotNil(t, item)
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(item.GetDomain()), "."))
		require.NotEmpty(t, host)
		if _, ok := requested[host]; !ok {
			t.Fatalf("resolver returned an unrequested domain %q", host)
		}
		require.False(t, resolved[host], "resolver returned duplicate domain %s", host)
		require.NotEmpty(t, item.GetIps())
		resolved[host] = true
		for _, candidate := range item.GetIps() {
			require.NotNil(t, candidate)
			parsed := net.ParseIP(candidate.GetIp())
			require.NotNil(t, parsed)
			require.NotNil(t, parsed.To4(), "resolver must return IPv4")
			require.Greater(t, candidate.GetTcpConnectLatencyMs(), uint32(0))
			require.LessOrEqual(t, candidate.GetTcpConnectLatencyMs(), uint32(10000))
			require.True(t, isPublicIPv4(parsed), "resolver must return public IPv4: %s", candidate.GetIp())
		}
		seen := make(map[string]struct{}, len(item.GetIps()))
		require.LessOrEqual(t, len(item.GetIps()), 4)
		var previousLatency uint32
		for _, candidate := range item.GetIps() {
			require.GreaterOrEqual(t, candidate.GetTcpConnectLatencyMs(), previousLatency, "resolver IPs must be latency ordered for %s", host)
			previousLatency = candidate.GetTcpConnectLatencyMs()
			if _, ok := seen[candidate.GetIp()]; ok {
				t.Fatalf("resolver returned duplicate IP %q for %s", candidate.GetIp(), host)
			}
			seen[candidate.GetIp()] = struct{}{}
		}
	}
	for _, domain := range domains {
		require.True(t, resolved[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))], "domain was unresolved: %s", domain)
	}
}

func isPublicIPv4(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	first, second, third := ip[0], ip[1], ip[2]
	return first != 0 && first < 224 &&
		!(first == 100 && second >= 64 && second <= 127) &&
		!(first == 192 && second == 0 && (third == 0 || third == 2)) &&
		!(first == 198 && (second == 18 || second == 19 || (second == 51 && third == 100))) &&
		!(first == 203 && second == 0 && third == 113)
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
