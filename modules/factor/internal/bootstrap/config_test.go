package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFactorConfigContainsOnlyRuntimeInputs(t *testing.T) {
	cfg := Default()
	require.Equal(t, 32, cfg.Engine.PythonWorkers)
	require.Equal(t, 64, cfg.Engine.ViewReadWorkers)
	require.Equal(t, 10000, cfg.Engine.ViewReadTimeoutMS)
	require.True(t, cfg.Engine.BatchEnabled)
	require.NotEmpty(t, cfg.Engine.PythonBin)
	require.NotEmpty(t, cfg.Engine.WorkerPath)
	require.NotEmpty(t, cfg.Engine.FactorsDir)
}

func TestBatchEnabledEnvOverride(t *testing.T) {
	t.Setenv("MOOX_FACTOR_ENGINE_BATCH_ENABLED", "false")
	cfg := Default()
	require.NoError(t, cfg.applyEnv())
	require.False(t, cfg.Engine.BatchEnabled)

	t.Setenv("MOOX_FACTOR_ENGINE_BATCH_ENABLED", "true")
	require.NoError(t, cfg.applyEnv())
	require.True(t, cfg.Engine.BatchEnabled)
}

func TestInvalidBatchEnabledEnvIsRejected(t *testing.T) {
	t.Setenv("MOOX_FACTOR_ENGINE_BATCH_ENABLED", "maybe")
	cfg := Default()
	require.Error(t, cfg.applyEnv())
	require.True(t, cfg.Engine.BatchEnabled)
}

func TestViewReadPipelineEnvOverrides(t *testing.T) {
	t.Setenv("MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS", "24")
	t.Setenv("MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS", "7500")
	cfg := Default()
	cfg.applyEnv()
	require.Equal(t, 24, cfg.Engine.ViewReadWorkers)
	require.Equal(t, 7500, cfg.Engine.ViewReadTimeoutMS)
}

func TestInvalidViewReadPipelineEnvKeepsDefaults(t *testing.T) {
	t.Setenv("MOOX_FACTOR_ENGINE_VIEW_READ_WORKERS", "0")
	t.Setenv("MOOX_FACTOR_ENGINE_VIEW_READ_TIMEOUT_MS", "-1")
	cfg := Default()
	cfg.applyEnv()
	require.Equal(t, 64, cfg.Engine.ViewReadWorkers)
	require.Equal(t, 10000, cfg.Engine.ViewReadTimeoutMS)
}

func writeConfig(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o644))
	return path
}

func TestLoadUsesPythonWorkersAsOnlyConcurrencySetting(t *testing.T) {
	cfg, err := Load(writeConfig(t, "engine:\n  python_workers: 100\n"))
	require.NoError(t, err)
	require.Equal(t, 100, cfg.Engine.PythonWorkers)
}

func TestLoadReadsBatchEnabledFromYAML(t *testing.T) {
	cfg, err := Load(writeConfig(t, "engine:\n  batch_enabled: false\n"))
	require.NoError(t, err)
	require.False(t, cfg.Engine.BatchEnabled)
}

func TestLoadRejectsLegacyWorkersAndScheduler(t *testing.T) {
	for _, raw := range []string{
		"engine:\n  workers: 24\n",
		"scheduler:\n  queue_capacity: 2048\n",
	} {
		_, err := Load(writeConfig(t, raw))
		require.Error(t, err)
	}
}

func TestPythonWorkersEnvOverride(t *testing.T) {
	t.Setenv("MOOX_FACTOR_ENGINE_PYTHON_WORKERS", "37")
	cfg := Default()
	cfg.applyEnv()
	require.Equal(t, 37, cfg.Engine.PythonWorkers)
}

func TestFactorEventBusCredentialEnvOverride(t *testing.T) {
	t.Setenv("MOOX_EVENTBUS_CREDENTIAL_FILE", "/tmp/shared.yaml")
	t.Setenv("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE", "/tmp/factor.yaml")
	cfg := Default()
	cfg.applyEnv()
	require.Equal(t, "/tmp/factor.yaml", cfg.EventBus.CredentialFile)
}

func TestLegacyWorkersEnvIsIgnored(t *testing.T) {
	t.Setenv("MOOX_FACTOR_ENGINE_WORKERS", "24")
	cfg := Default()
	cfg.applyEnv()
	require.Equal(t, 32, cfg.Engine.PythonWorkers)
}

func TestLoadWorkerPathFromYAMLAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("engine:\n  worker_path: ./yaml-worker.py\n"), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "./yaml-worker.py", cfg.Engine.WorkerPath)

	t.Setenv("MOOX_FACTOR_ENGINE_WORKER_PATH", "/tmp/env-worker.py")
	cfg, err = Load(path)
	require.NoError(t, err)
	require.Equal(t, "/tmp/env-worker.py", cfg.Engine.WorkerPath)
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("engine:\n  sections_dir: ./sections\n"), 0o644))
	_, err := Load(path)
	require.Error(t, err)
}
