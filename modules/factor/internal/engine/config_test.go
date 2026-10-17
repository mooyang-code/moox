package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const minimalEngineConfig = `gateway_client:
  access_address: storage.example:11004
  access_id: access@storage
  caller: factor-engine
  key_id: assigned-factor-engine-47
  key_file: ../secrets/access-factor-engine.key
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
	require.Equal(t, "../secrets/access-factor-engine.key", cfg.GatewayClient.KeyFile)
	require.Equal(t, "assigned-factor-engine-47", cfg.GatewayClient.KeyID)
	require.Equal(t, filepath.Join(root, "secrets/storage-primary-auth.secret"), cfg.Storage.AuthSecretFile)
	require.Equal(t, 500, cfg.Recalc.ChunkPeriods)
	require.Equal(t, ByteSize(1<<30), cfg.Compute.MaxMemory)
}

func TestEngineConfigRejectsDatabaseSection(t *testing.T) {
	_, err := Load(writeEngineConfig(t, minimalEngineConfig+"database:\n  path: ./factor.db\n"))
	require.Error(t, err)
}

func TestEngineConfigRequiresCompleteExternalGateway(t *testing.T) {
	_, err := Load(writeEngineConfig(t, "eventbus:\n  urls: [nats://127.0.0.1:4222]\n"))
	require.ErrorContains(t, err, "access address")
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

// 旧配置的 python 段会让启动失败（严格解析），发布时必须同步改配置。
func TestEngineConfigRejectsRetiredPythonSection(t *testing.T) {
	_, err := Load(writeEngineConfig(t, minimalEngineConfig+"python:\n  bin: ./venv/bin/python\n"))
	require.ErrorContains(t, err, "python")
}

func TestEngineConfigComputeSection(t *testing.T) {
	cfg, err := Load(writeEngineConfig(t, minimalEngineConfig+"compute:\n  workers: 2\n  max_memory: 512MiB\n"))
	require.NoError(t, err)
	require.Equal(t, 2, cfg.Compute.Workers)
	require.Equal(t, ByteSize(512<<20), cfg.Compute.MaxMemory)
	cfg, err = Load(writeEngineConfig(t, minimalEngineConfig+"compute:\n  max_memory: 1073741824\n"))
	require.NoError(t, err)
	require.Equal(t, ByteSize(1<<30), cfg.Compute.MaxMemory)
	for _, bad := range []string{"1.5GiB", "-1MiB", "lots", "1 TB"} {
		_, err = Load(writeEngineConfig(t, minimalEngineConfig+"compute:\n  max_memory: "+bad+"\n"))
		require.ErrorContains(t, err, "字节数", bad)
	}
	_, err = Load(writeEngineConfig(t, minimalEngineConfig+"compute:\n  workers: -1\n"))
	require.ErrorContains(t, err, "compute.workers")
}

// 空洞防护默认关闭，默认值与账本目录按配置根目录解析；不合法的取值被拒绝。
func TestEngineConfigGapDefaultsAndValidation(t *testing.T) {
	path := writeEngineConfig(t, minimalEngineConfig)
	root := filepath.Dir(filepath.Dir(path))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.False(t, cfg.Gap.Enabled, "默认关闭")
	require.Equal(t, filepath.Join(root, "data/gap"), cfg.Gap.LedgerDir)
	require.Equal(t, 5*time.Minute, cfg.Gap.SweepInterval)
	require.Equal(t, 6*time.Hour, cfg.Gap.GiveUpAfter)
	require.Equal(t, 20, cfg.Gap.MaxGroupsPerSweep)
	require.Equal(t, 60, cfg.Gap.MaxSpan)
	require.InDelta(t, 0.2, cfg.Gap.MassRatio, 1e-9)

	bad := writeEngineConfig(t, minimalEngineConfig+"\ngap:\n  mass_ratio: 1.5\n")
	_, err = Load(bad)
	require.ErrorContains(t, err, "gap.mass_ratio")
}
