package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func roleConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestRoleConfigurationsHaveIndependentStorageAndStrictFields(t *testing.T) {
	control, err := LoadControlConfig(roleConfigFile(t, "{}"))
	require.NoError(t, err)
	engine, err := LoadEngineApplicationConfig(roleConfigFile(t, "{}"))
	require.NoError(t, err)
	require.NotEqual(t, control.Database.Path, engine.Database.Path)
	require.Equal(t, 2233*time.Second, engine.Cache.CheckInterval)
	require.Equal(t, 10*time.Minute, engine.CatalogSyncTimeout)
	require.Equal(t, 200*time.Millisecond, engine.SubjectBatch.Window)
	require.Equal(t, 64, engine.SubjectBatch.MaxBatch)
	_, err = LoadEngineApplicationConfig(roleConfigFile(t, "subject_batch:\n  max_batch: 0\n"))
	require.ErrorContains(t, err, "subject batch")
	_, err = LoadControlConfig(roleConfigFile(t, "engine:\n  python_workers: 64\n"))
	require.Error(t, err)
	_, err = LoadControlConfig(roleConfigFile(t, "{}\n---\n{}\n"))
	require.Error(t, err)
	_, err = LoadEngineApplicationConfig(roleConfigFile(t, "cache:\n  rebuild_keep_rows: 0\n"))
	require.ErrorContains(t, err, "rebuild_keep_rows")
	_, err = LoadEngineApplicationConfig(roleConfigFile(t, "catalog_poll_interval: 0s\n"))
	require.ErrorContains(t, err, "catalog_poll_interval")
	_, err = LoadEngineApplicationConfig(roleConfigFile(t, "catalog_sync_timeout: 0s\n"))
	require.ErrorContains(t, err, "catalog_sync_timeout")
}

func TestRoleConfigurationsApplyProcessEnvironment(t *testing.T) {
	t.Setenv("MOOX_FACTOR_DB_PATH", "/tmp/control-catalog.db")
	t.Setenv("MOOX_FACTOR_ENGINE_FACTORS_DIR", "/tmp/control-artifacts")
	t.Setenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID", "storage-from-env")
	t.Setenv("MOOX_FACTOR_ENGINE_DB_PATH", "/tmp/engine-runtime.db")
	t.Setenv("MOOX_FACTOR_ENGINE_ID", "engine-from-env")
	control, err := LoadControlConfig(roleConfigFile(t, "{}"))
	require.NoError(t, err)
	require.Equal(t, "/tmp/control-catalog.db", control.Database.Path)
	require.Equal(t, "/tmp/control-artifacts", control.ArtifactsDir)
	require.Equal(t, "storage-from-env", control.Storage.GatewayNodeID)
	engine, err := LoadEngineApplicationConfig(roleConfigFile(t, "{}"))
	require.NoError(t, err)
	require.Equal(t, "/tmp/engine-runtime.db", engine.Database.Path)
	require.Equal(t, "engine-from-env", engine.EngineID)
	require.Equal(t, "/tmp/control-artifacts", engine.Engine.FactorsDir)
	require.Equal(t, "storage-from-env", engine.Storage.GatewayNodeID)
}
