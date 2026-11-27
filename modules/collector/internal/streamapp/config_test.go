package streamapp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeStreamConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadStreamConfigIsStrictAndResolvesRelativePaths(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "Crypto")
	path := writeStreamConfig(t, "gateway_client: {caller: collector-stream, key_file: k}\n")
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "crypto", cfg.SpaceID, "空间可由部署环境给出")
	require.Empty(t, cfg.MarketConfig, "默认使用二进制内嵌 binance.yaml，不依赖外部文件")
	require.Equal(t, 4, cfg.LocalExecutor.Concurrency)
	require.NotEmpty(t, cfg.InstanceID)

	// 兼容旧配置中的 market_config：指向不存在的外部文件时自动回退内嵌配置；文件存在时保留覆盖路径。
	legacyPath := writeStreamConfig(t, "gateway_client: {caller: collector-stream, key_file: k}\nmarket_config: sources/market/binance.yaml\n")
	legacyCfg, err := Load(legacyPath)
	require.NoError(t, err)
	require.Empty(t, legacyCfg.MarketConfig)

	overridePath := writeStreamConfig(t, "gateway_client: {caller: collector-stream, key_file: k}\nmarket_config: custom.yaml\n")
	overrideFile := filepath.Join(filepath.Dir(overridePath), "custom.yaml")
	require.NoError(t, os.WriteFile(overrideFile, []byte("app: {}\n"), 0o600))
	overrideCfg, err := Load(overridePath)
	require.NoError(t, err)
	require.Equal(t, overrideFile, overrideCfg.MarketConfig)

	_, err = Load(writeStreamConfig(t, "gateway_client: {caller: collector-stream}\nunknown: 1\n"))
	require.Error(t, err, "未知字段拒绝")
	_, err = Load(writeStreamConfig(t, "gateway_client: {caller: collector}\n"))
	require.ErrorContains(t, err, "collector-stream")
	t.Setenv("MOOX_SPACE_ID", "")
	_, err = Load(writeStreamConfig(t, "gateway_client: {caller: collector-stream, key_file: k}\n"))
	require.ErrorContains(t, err, "space_id")
	_, err = Load(writeStreamConfig(t, "gateway_client: {caller: collector-stream, key_file: k}\nspace_id: crypto\nlocal_executor: {concurrency: 0}\n"))
	require.Error(t, err)
}

// 仓库里的配置模板本身能通过严格解析（无需外部 binance.yaml）。
func TestStreamConfigTemplateParses(t *testing.T) {
	raw, err := os.ReadFile("../../config/stream/app.yaml")
	require.NoError(t, err)
	t.Setenv("MOOX_SPACE_ID", "crypto")
	_, err = Load(writeStreamConfig(t, string(raw)))
	require.NoError(t, err)
}
