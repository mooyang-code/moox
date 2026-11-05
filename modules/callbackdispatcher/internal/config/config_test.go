package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestShippedConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config", "app.yaml"))
	require.NoError(t, err)
	require.Equal(t, "callback-dispatcher", cfg.GatewayClient.Caller)
	require.Equal(t, ":11461", cfg.Health.Addr)
	require.Equal(t, 24*time.Hour, cfg.Delivery.RetryWindow)
	require.Equal(t, 3*time.Second, cfg.Delivery.ConnectTimeout)
	require.Equal(t, 10*time.Second, cfg.Delivery.RequestTimeout)
	require.Equal(t, 30, cfg.Retention.ExecutionDays)
	require.Contains(t, cfg.EventBus.CredentialFile, "callback-dispatcher-eventbus.yaml")
}

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadRejectsUnknownFieldsAndInvalidValues(t *testing.T) {
	_, err := Load(write(t, "unknown_field: true\n"))
	require.Error(t, err)
	_, err = Load(write(t, "database:\n  path: a.db\n---\ndatabase:\n  path: b.db\n"))
	require.Error(t, err)
	_, err = Load(write(t, "gateway_client:\n  caller: trade\n"))
	require.ErrorContains(t, err, "callback-dispatcher")
	for name, content := range map[string]string{
		"批大小":   "delivery:\n  worker_batch: 0\n",
		"请求超时":  "delivery:\n  connect_timeout: 5s\n  request_timeout: 1s\n",
		"重试窗口":  "delivery:\n  retry_window: 1s\n",
		"保留天数":  "retention:\n  execution_days: 0\n",
		"没有总线地址": "eventbus:\n  urls: []\n",
	} {
		_, err := Load(write(t, content))
		require.Error(t, err, name)
	}
	_, err = Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
}

func TestDatabasePathCanBeOverriddenByEnvironment(t *testing.T) {
	t.Setenv("MOOX_CALLBACK_DISPATCHER_DB_PATH", "/tmp/override.db")
	cfg, err := Load(write(t, "{}\n"))
	require.NoError(t, err)
	require.Equal(t, "/tmp/override.db", cfg.Database.Path)
}
