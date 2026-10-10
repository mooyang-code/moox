package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStrictConfigurationAndBoundedDNS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	load := func(raw string) (Config, error) {
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		return Load(path)
	}
	cfg, err := load("domains: ['*.binance.com']\ndns:\n  domains: ['api.binance.com']\n")
	require.NoError(t, err)
	require.Equal(t, 1500, cfg.DNS.LookupTimeoutMS)
	require.Equal(t, 443, cfg.DNS.ProbePort)
	for _, raw := range []string{
		"unknown: true\n", "domains: []\ndomains: []\n", "domains: []\n---\n", "[]\n", "",
		"domains: ['*']\n", "dns:\n  domains: ['*.binance.com']\n",
		"dns:\n  domains: ['api.binance.com', 'API.BINANCE.COM']\n",
		"dns:\n  probe_port: 65536\n", "dns:\n  cache_ttl_seconds: 0\n",
		"dns:\n  lookup_timeout_ms: 60001\n", "dns:\n  max_ips_per_domain: 5\n",
	} {
		_, err := load(raw)
		require.Error(t, err, raw)
	}
}

func TestShippedConfiguration(t *testing.T) {
	_, err := Load("../../config/app.yaml")
	require.NoError(t, err)
}
