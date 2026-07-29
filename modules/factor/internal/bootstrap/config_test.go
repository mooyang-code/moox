package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFactorConfigContainsOnlyRuntimeInputs(t *testing.T) {
	cfg := Default()
	require.Equal(t, 2048, cfg.Scheduler.QueueCapacity)
	require.Equal(t, 1, cfg.Scheduler.MaxRetry)
	require.NotEmpty(t, cfg.Engine.PythonBin)
	require.NotEmpty(t, cfg.Engine.WorkerPath)
	require.NotEmpty(t, cfg.Engine.FactorsDir)
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
