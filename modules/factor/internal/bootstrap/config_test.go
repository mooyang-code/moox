package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFactorConfigContainsOnlyRuntimeInputs(t *testing.T) {
	cfg := Default()
	require.Equal(t, 2048, cfg.Scheduler.QueueCapacity)
	require.Equal(t, 1, cfg.Scheduler.MaxRetry)
	require.Equal(t, 300*time.Millisecond, cfg.Scheduler.ViewSettleDelay)
	require.Equal(t, 3, cfg.Scheduler.EventReadRetry)
	require.Equal(t, 500*time.Millisecond, cfg.Scheduler.EventReadRetryInterval)
	require.NotEmpty(t, cfg.Engine.PythonBin)
	require.NotEmpty(t, cfg.Engine.WorkerPath)
	require.NotEmpty(t, cfg.Engine.FactorsDir)
}

func TestLoadSchedulerViewReadinessFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"scheduler:\n"+
			"  view_settle_delay: 25ms\n"+
			"  event_read_retry: 2\n"+
			"  event_read_retry_interval: 40ms\n",
	), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 25*time.Millisecond, cfg.Scheduler.ViewSettleDelay)
	require.Equal(t, 2, cfg.Scheduler.EventReadRetry)
	require.Equal(t, 40*time.Millisecond, cfg.Scheduler.EventReadRetryInterval)
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
