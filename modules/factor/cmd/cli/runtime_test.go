package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestLoadRuntimeConfigUsesPythonAndPipelineSections(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.yaml")
	contents := "database:\n  path: ./state/factor.db\npython:\n  bin: python311\n  worker_path: ./worker.py\n  factors_dir: ./factors\n  workers: 6\n  task_timeout: 45s\npipeline:\n  read_workers: 3\n  read_timeout: 9s\n  write_batch_rows: 700\n"
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	cfg, err := loadRuntimeConfig(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "state/factor.db"), cfg.DatabasePath)
	require.Equal(t, filepath.Join(root, "worker.py"), cfg.WorkerPath)
	require.Equal(t, filepath.Join(root, "factors"), cfg.FactorsDir)
	require.Equal(t, "python311", cfg.PythonBin)
	require.Equal(t, 6, cfg.PythonWorkers)
	require.Equal(t, 3, cfg.ReadWorkers)
	require.Equal(t, 9*time.Second, cfg.ReadTimeout)
	require.Equal(t, 700, cfg.WriteBatchRows)
	require.Equal(t, 45*time.Second, cfg.TaskTimeout)
}

func TestLoadRuntimeConfigPythonEnvironmentOverridesConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("python:\n  bin: configured-python\n"), 0o600))
	t.Setenv("MOOX_FACTOR_PYTHON_BIN", "env-python")
	t.Setenv("MOOX_FACTOR_PYTHON_WORKER_PATH", "/opt/worker.py")
	t.Setenv("MOOX_FACTOR_PYTHON_FACTORS_DIR", "/opt/factors")
	t.Setenv("MOOX_FACTOR_STORAGE_GATEWAY_TARGET", "ip://127.0.0.1:11003")
	t.Setenv("MOOX_FACTOR_STORAGE_GATEWAY_NODE_ID", "storage-node-0")

	cfg, err := loadRuntimeConfig(path)
	require.NoError(t, err)
	require.Equal(t, "env-python", cfg.PythonBin)
	require.Equal(t, "/opt/worker.py", cfg.WorkerPath)
	require.Equal(t, "/opt/factors", cfg.FactorsDir)
	require.Equal(t, "ip://127.0.0.1:11003", cfg.GatewayTarget)
	require.Equal(t, "storage-node-0", cfg.GatewayNodeID)
}

func TestValidateImportableSetRejectsPendingAndDeleting(t *testing.T) {
	for _, status := range []string{domain.SetStatusPending, domain.SetStatusDeleting} {
		err := validateImportableSet(domain.FactorSet{Status: status})
		require.Error(t, err)
	}
	require.NoError(t, validateImportableSet(domain.FactorSet{Status: domain.SetStatusEnabled}))
	require.NoError(t, validateImportableSet(domain.FactorSet{Status: domain.SetStatusDisabled}))
}

func TestImportLockUsesServiceDatabaseLockDirectory(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "factor.db")
	unlock, err := lockImportSet(context.Background(), databasePath, "set_prices")
	require.NoError(t, err)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = catalog.NewLocks(databasePath+".locks").LockContext(ctx, "set_prices")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
