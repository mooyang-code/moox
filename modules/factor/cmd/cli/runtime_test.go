package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/stretchr/testify/require"
)

func TestLoadRuntimeConfigReadsDatabaseGatewayClientAndPython(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	path := filepath.Join(configDir, "app.yaml")
	contents := "database:\n  path: ./state/factor.db\ngateway_client:\n  mode: local\n  caller: factor-mgr\n  key_file: ../secrets/caller-factor-mgr.key\n  ca_file: ../certs/moox-ca.crt\n  cache_dir: ./data/gatewayclient\npython:\n  bin: python311\n"
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	cfg, err := loadRuntimeConfig(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "state/factor.db"), cfg.DatabasePath)
	require.Equal(t, "factor-mgr", cfg.GatewayClient.Caller)
	require.Equal(t, filepath.Join(filepath.Dir(root), "secrets/caller-factor-mgr.key"), cfg.GatewayClient.KeyFile, "相对路径按组件目录解析")
	require.Equal(t, filepath.Join(root, "data/gatewayclient"), cfg.GatewayClient.CacheDir)
	require.Equal(t, "python311", cfg.PythonBin)
}

func TestLoadRuntimeConfigEnvironmentOverridesConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("python:\n  bin: configured-python\n"), 0o600))
	t.Setenv("MOOX_FACTOR_PYTHON_BIN", "env-python")

	cfg, err := loadRuntimeConfig(path)
	require.NoError(t, err)
	require.Equal(t, "env-python", cfg.PythonBin)
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
	unlock, err := lockImport(context.Background(), databasePath, []string{"momentum", "bias", "momentum"}, "set_prices")
	require.NoError(t, err)
	defer unlock()
	locks := setlock.New(databasePath + ".locks")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = locks.LockContext(ctx, "set_prices")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = locks.LockFactorContext(ctx, "bias")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
