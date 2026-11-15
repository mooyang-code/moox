package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStrictConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	load := func(raw string) (Config, error) {
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		return Load(path)
	}
	cfg, err := load("domains: ['*.binance.com']\n")
	require.NoError(t, err)
	require.Equal(t, []string{"*.binance.com"}, cfg.Domains)
	for _, raw := range []string{
		"unknown: true\n", "domains: []\ndomains: []\n", "domains: []\n---\n", "[]\n", "",
		"domains: ['*']\n", "dns:\n  domains: ['api.binance.com']\n",
	} {
		_, err := load(raw)
		require.Error(t, err, raw)
	}
}

func TestShippedConfiguration(t *testing.T) {
	_, err := Load("../../config/app.yaml")
	require.NoError(t, err)
}
