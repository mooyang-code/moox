package binance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveBinanceConfigsLoadFromEmbeddedYAML(t *testing.T) {
	t.Setenv("MOOX_STORAGE_MARKET_CONFIG", "")
	// 切到无关临时目录，确认完全不依赖外部文件系统中的 binance.yaml。
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(t.TempDir()))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	apiCfg, err := ResolveAPIConfig()
	require.NoError(t, err)
	require.Equal(t, []string{"https://api.binance.com", "https://api-gcp.binance.com", "https://data-api.binance.vision"}, apiCfg.SpotBaseURLs)
	require.Equal(t, []string{"https://fapi.binance.com"}, apiCfg.SwapBaseURLs)

	streamCfg, err := ResolveStreamConfig()
	require.NoError(t, err)
	require.Equal(t, []string{"wss://stream.binance.com:9443/stream", "wss://stream.binance.com:443/stream"}, streamCfg.SpotEndpoints)
	require.Equal(t, []string{"wss://fstream.binance.com/market/stream"}, streamCfg.SwapEndpoints)
	require.Equal(t, 1000, streamCfg.StreamsPerConn)

	// 指向不存在路径时自动回退到内嵌配置。
	t.Setenv("MOOX_STORAGE_MARKET_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	apiCfg, err = ResolveAPIConfig()
	require.NoError(t, err)
	require.NotEmpty(t, apiCfg.SpotBaseURLs)

	// 指向存在的覆盖文件时优先使用覆盖文件。
	override := filepath.Join(t.TempDir(), "override.yaml")
	require.NoError(t, os.WriteFile(override, []byte("api:\n  spot_base_urls:\n    - https://override.example.com\n"), 0o600))
	t.Setenv("MOOX_STORAGE_MARKET_CONFIG", override)
	apiCfg, err = ResolveAPIConfig()
	require.NoError(t, err)
	require.Equal(t, []string{"https://override.example.com"}, apiCfg.SpotBaseURLs)
}
