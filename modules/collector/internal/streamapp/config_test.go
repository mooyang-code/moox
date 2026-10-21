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
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sources", "market"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sources", "market", "binance.yaml"), []byte("app: {}\n"), 0o600))
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
	require.Equal(t, filepath.Join(filepath.Dir(path), "sources/market/binance.yaml"), cfg.MarketConfig)
	require.Equal(t, 4, cfg.LocalExecutor.Concurrency)
	require.NotEmpty(t, cfg.InstanceID)

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

// 仓库里的配置模板本身能通过严格解析（数据源配置在部署时放到 config/sources 下）。
func TestStreamConfigTemplateParses(t *testing.T) {
	raw, err := os.ReadFile("../../config/stream/app.yaml")
	require.NoError(t, err)
	t.Setenv("MOOX_SPACE_ID", "crypto")
	_, err = Load(writeStreamConfig(t, string(raw)))
	require.NoError(t, err)
}
