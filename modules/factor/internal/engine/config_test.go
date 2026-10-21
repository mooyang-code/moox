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

// 账本的默认值与目录按配置根目录解析；本地存储、窗口、内存、同步的默认值与校验。
func TestEngineConfigGapSeriesDefaultsAndValidation(t *testing.T) {
	path := writeEngineConfig(t, minimalEngineConfig)
	root := filepath.Dir(filepath.Dir(path))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "data/gap"), cfg.Gap.LedgerDir)
	require.Equal(t, 5*time.Minute, cfg.Gap.SweepInterval)
	require.Equal(t, 6*time.Hour, cfg.Gap.GiveUpAfter)
	require.Equal(t, 20, cfg.Gap.MaxGroupsPerSweep)
	require.Equal(t, 60, cfg.Gap.MaxSpan)

	require.Equal(t, filepath.Join(root, "data/seriesstore"), cfg.SeriesStore.Dir)
	require.Equal(t, ByteSize(512<<20), cfg.SeriesStore.BlockCache)
	require.Equal(t, 4, cfg.Sync.Workers)
	require.Equal(t, 100, cfg.Sync.BatchSubjects)
	require.Equal(t, 30*time.Second, cfg.Sync.FollowInterval)
	require.Zero(t, cfg.Window.MaxMemory, "默认不限制，初始化时按探测结果写入")
	require.Zero(t, cfg.Memory.TotalBudget)

	cfg, err = Load(writeEngineConfig(t, minimalEngineConfig+"seriesstore:\n  dir: /data/series\n  block_cache: 1GiB\nwindow:\n  max_memory: 4GiB\n  headroom: 64\nmemory:\n  total_budget: 12GiB\nsync:\n  workers: 2\n  batch_subjects: 50\n  follow_interval: 10s\n"))
	require.NoError(t, err)
	require.Equal(t, "/data/series", cfg.SeriesStore.Dir)
	require.Equal(t, ByteSize(1<<30), cfg.SeriesStore.BlockCache)
	require.Equal(t, ByteSize(4<<30), cfg.Window.MaxMemory)
	require.Equal(t, 64, cfg.Window.Headroom)
	require.Equal(t, ByteSize(12<<30), cfg.Memory.TotalBudget)
	require.Equal(t, 50, cfg.Sync.BatchSubjects)

	for _, bad := range []struct{ body, want string }{
		{"\ngap:\n  max_span: -1\n", "gap.max_groups_per_sweep and gap.max_span"},
		{"\nsync:\n  batch_subjects: 101\n", "sync.batch_subjects"},
		{"\nwindow:\n  headroom: -1\n", "window.headroom"},
		{"\npipeline:\n  read_workers: 4\n", "read_workers"}, // 旧的读取配置已经删除：严格解析拒绝未知字段
		{"\ngap:\n  mass_ratio: 0.2\n", "mass_ratio"},
	} {
		_, err = Load(writeEngineConfig(t, minimalEngineConfig+bad.body))
		require.ErrorContains(t, err, bad.want, bad.body)
	}
}
