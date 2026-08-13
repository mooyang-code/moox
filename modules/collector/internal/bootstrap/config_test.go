package bootstrap

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultHealthConfigAndEnvOverride(t *testing.T) {
	t.Setenv("MOOX_COLLECTOR_HEALTH_ADDR", "127.0.0.1:16012")

	cfg := Default()
	if cfg.Health.Addr != ":11412" {
		t.Fatalf("Health.Addr = %q, want %q", cfg.Health.Addr, ":11412")
	}

	cfg.applyEnv()
	if cfg.Health.Addr != "127.0.0.1:16012" {
		t.Fatalf("Health.Addr = %q, want %q", cfg.Health.Addr, "127.0.0.1:16012")
	}
}

func TestLoadReadsYAMLAndAppliesEnvOverrides(t *testing.T) {
	t.Setenv("MOOX_COLLECTOR_DB_PATH", "./override/collector.db")
	t.Setenv("MOOX_COLLECTOR_HEALTH_ADDR", "127.0.0.1:16012")
	t.Setenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET", "ip://127.0.0.1:30100")

	path := writeCollectorConfig(t, `
database:
  path: ./original/collector.db
storage:
  gateway_target: ip://127.0.0.1:20100
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "./override/collector.db", cfg.Database.Path)
	assert.Equal(t, "127.0.0.1:16012", cfg.Health.Addr)
	assert.Equal(t, "ip://127.0.0.1:30100", cfg.Storage.GatewayTarget)
}

func TestLoadRejectsLegacyStorageTargets(t *testing.T) {
	_, err := Load(writeCollectorConfig(t, `
storage:
  metadata_target: 127.0.0.1:20100
  access_target: 127.0.0.1:20102
`))
	require.Error(t, err)
}

func TestLoadRejectsMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read config")
}

func TestLoadDNSResolverConfigAndEnvironment(t *testing.T) {
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_ENABLED", "true")
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_TARGET", "ip://43.132.204.177:11003")
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_NODE_ID", "compute-1")
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_DOMAINS", "fapi.binance.com,api.binance.com")
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_REFRESH_INTERVAL", "5m")
	t.Setenv("MOOX_COLLECTOR_DNS_RESOLVER_TIMEOUT", "3s")

	cfg, err := Load(writeCollectorConfig(t, "database:\n  path: ./collector.db\n"))
	require.NoError(t, err)
	assert.True(t, cfg.DNSResolver.Enabled)
	assert.Equal(t, "compute-1", cfg.DNSResolver.NodeID)
	assert.Equal(t, []string{"fapi.binance.com", "api.binance.com"}, cfg.DNSResolver.Domains)
}

func TestLoadRejectsEnabledDNSResolverWithoutNode(t *testing.T) {
	_, err := Load(writeCollectorConfig(t, `dns_resolver:
  enabled: true
  target: ip://43.132.204.177:11003
  domains: [fapi.binance.com]
  refresh_interval: 5m
  request_timeout: 3s
  cache_ttl: 5m
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dns_resolver.node_id")
}

func TestLoadRejectsNonPublicDNSResolverTargets(t *testing.T) {
	for _, target := range []string{"ip://127.0.0.1:11003", "ip://10.0.0.5:11003", "ip://localhost:11003", "ip://not-a-public-host:11003", "http://43.132.204.177:11003"} {
		t.Run(target, func(t *testing.T) {
			_, err := Load(writeCollectorConfig(t, "dns_resolver:\n  enabled: true\n  target: "+target+"\n  node_id: compute-1\n  domains: [fapi.binance.com]\n  refresh_interval: 5m\n  request_timeout: 3s\n  cache_ttl: 5m\n"))
			require.Error(t, err)
			require.Contains(t, err.Error(), "dns_resolver.target")
		})
	}
}

func writeCollectorConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}
