package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadShippedConfig(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config", "app.yaml"))
	require.NoError(t, err)
	require.Equal(t, []string{"*.binance.com", "data-api.binance.vision"}, cfg.HTTP.Domains)
	require.Equal(t, int64(32<<20), cfg.HTTP.MaxResponseBytes)
	require.Equal(t, 15*time.Second, cfg.HTTP.DefaultTimeout)
	require.Len(t, cfg.DNS.Domains, 4)
	require.Equal(t, 1500*time.Millisecond, cfg.DNS.LookupTimeout)
	require.Equal(t, 4, cfg.DNS.MaxIPsPerDomain)
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	for name, body := range map[string]string{
		"未知字段":      "http:\n  domains: [api.binance.com]\n  proxy: http://127.0.0.1:7890\n",
		"空白名单":      "http:\n  domains: []\n",
		"白名单带协议":    "http:\n  domains: [https://api.binance.com]\n",
		"默认超时过长":    "http:\n  domains: [api.binance.com]\n  default_timeout: 2m\n",
		"DNS 域名无效":  "http:\n  domains: [api.binance.com]\ndns:\n  domains: [1.2.3.4]\n",
		"DNS 地址数过多": "http:\n  domains: [api.binance.com]\ndns:\n  max_ips_per_domain: 5\n",
	} {
		path := filepath.Join(t.TempDir(), "app.yaml")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		_, err := Load(path)
		require.Error(t, err, name)
	}
}
