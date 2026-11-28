package binance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/stretchr/testify/require"
)

func TestResolveBinanceConfigsLoadFromEmbeddedYAML(t *testing.T) {
	t.Setenv(markets.ConfigDirEnv, "")
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

	// 覆盖目录里没有该文件时自动回退到内嵌配置。
	t.Setenv(markets.ConfigDirEnv, t.TempDir())
	apiCfg, err = ResolveAPIConfig()
	require.NoError(t, err)
	require.NotEmpty(t, apiCfg.SpotBaseURLs)

	// MOOX_MARKET_CONFIG_DIR 里存在 crypto/sources/binance.yaml 时优先使用覆盖文件。
	overrideDir := t.TempDir()
	overrideFile := filepath.Join(overrideDir, "crypto", "sources", "binance.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(overrideFile), 0o755))
	require.NoError(t, os.WriteFile(overrideFile, []byte("api:\n  spot_base_urls:\n    - https://override.example.com\n"), 0o600))
	t.Setenv(markets.ConfigDirEnv, overrideDir)
	apiCfg, err = ResolveAPIConfig()
	require.NoError(t, err)
	require.Equal(t, []string{"https://override.example.com"}, apiCfg.SpotBaseURLs)
}
