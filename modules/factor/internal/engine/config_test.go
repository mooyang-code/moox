package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const minimalEngineConfig = `gateway_client:
  key_file: ./secrets/caller-factor-engine.key
  access_address: 146.56.196.204:11004
  access_id: access@storage
eventbus:
  urls: [tls://control.example:4222]
`

func writeEngineConfig(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "config"), 0o755))
	path := filepath.Join(root, "config", "engine.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestEngineConfigDefaultsAndPaths(t *testing.T) {
	path := writeEngineConfig(t, minimalEngineConfig)
	root := filepath.Dir(filepath.Dir(path))

	cfg, err := Load(path)

	require.NoError(t, err)
	require.Contains(t, cfg.Engine.ID, "factor-engine@")
	require.Equal(t, 10*time.Second, cfg.Engine.HeartbeatInterval)
	require.Equal(t, time.Minute, cfg.CatalogSync.Interval)
	require.Equal(t, 45*time.Second, cfg.CatalogSync.Offset)
	require.Equal(t, filepath.Join(root, "data/engine/catalog.json"), cfg.CatalogSync.StateFile)
	require.Equal(t, filepath.Join(root, "secrets/caller-factor-engine.key"), cfg.GatewayClient.KeyFile)
	require.Equal(t, "access", string(cfg.GatewayClient.Mode))
	require.Equal(t, "factor-engine", cfg.GatewayClient.Caller)
	require.Equal(t, 30*time.Second, cfg.Manager.Timeout)
	require.Equal(t, 500, cfg.Recalc.ChunkPeriods)
	require.Equal(t, filepath.Join(root, "pyworker/worker.py"), cfg.Python.WorkerPath)
}

func TestEngineConfigRejectsDatabaseSection(t *testing.T) {
	_, err := Load(writeEngineConfig(t, minimalEngineConfig+"database:\n  path: ./factor.db\n"))
	require.Error(t, err)
}

func TestEngineConfigRequiresAccessGatewayClient(t *testing.T) {
	_, err := Load(writeEngineConfig(t, "eventbus:\n  urls: [nats://127.0.0.1:4222]\n"))
	require.ErrorContains(t, err, "access_address")
	require.ErrorContains(t, err, "gateway_client.key_file")

	_, err = Load(writeEngineConfig(t, minimalEngineConfig+"manager:\n  url: https://control.example:11001\n"))
	require.Error(t, err, "旧的 manager.url 等网关配置已删除")

	local := "gateway_client:\n  mode: local\n  caller: factor-engine\n  key_file: ./k\n  ca_file: ./ca\n  cache_dir: ./cache\neventbus:\n  urls: [tls://control.example:4222]\n"
	_, err = Load(writeEngineConfig(t, local))
	require.ErrorContains(t, err, "gateway_client.mode must be access")
}

func TestEngineConfigRejectsOffsetNotLessThanInterval(t *testing.T) {
	_, err := Load(writeEngineConfig(t, minimalEngineConfig+"catalog_sync:\n  interval: 1m\n  offset: 60s\n"))
	require.ErrorContains(t, err, "catalog_sync.offset")
}

func TestEngineConfigAllowsZeroOffset(t *testing.T) {
	cfg, err := Load(writeEngineConfig(t, minimalEngineConfig+"catalog_sync:\n  offset: 0s\n"))
	require.NoError(t, err)
	require.Zero(t, cfg.CatalogSync.Offset)
}

func TestEngineConfigEngineIDFromEnvironment(t *testing.T) {
	t.Setenv("MOOX_FACTOR_ENGINE_ID", "factor-engine@mac")
	cfg, err := Load(writeEngineConfig(t, minimalEngineConfig))
	require.NoError(t, err)
	require.Equal(t, "factor-engine@mac", cfg.Engine.ID)
}

func TestEngineConfigResolvesRelativePythonBin(t *testing.T) {
	path := writeEngineConfig(t, minimalEngineConfig+"python:\n  bin: ./venv/bin/python\n")
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(filepath.Dir(filepath.Dir(path)), "venv/bin/python"), cfg.Python.Bin)

	cfg, err = Load(writeEngineConfig(t, minimalEngineConfig+"python:\n  bin: python3\n"))
	require.NoError(t, err)
	require.Equal(t, "python3", cfg.Python.Bin, "a bare command is looked up on PATH")
}
