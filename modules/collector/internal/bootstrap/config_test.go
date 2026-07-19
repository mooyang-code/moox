package bootstrap

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultHealthConfigAndEnvOverride(t *testing.T) {
	t.Setenv("MOOX_COLLECTOR_HEALTH_ADDR", "127.0.0.1:16012")

	cfg := Default()
	if cfg.Health.Addr != ":11412" {
		t.Fatalf("Health.Addr = %q, want %q", cfg.Health.Addr, ":11412")
	}

	cfg.applyEnv()
	if cfg.Health.Addr != "127.0.0.1:16012" {
		t.Fatalf("Health.Addr = %q, want %q", cfg.Health.Addr, "127.0.0.1:16012")
	}
}

func TestLoadReadsYAMLAndAppliesEnvOverrides(t *testing.T) {
	t.Setenv("MOOX_COLLECTOR_DB_PATH", "./override/collector.db")
	t.Setenv("MOOX_COLLECTOR_HEALTH_ADDR", "127.0.0.1:16012")
	t.Setenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET", "ip://127.0.0.1:30100")

	path := writeCollectorConfig(t, `
database:
  path: ./original/collector.db
storage:
  gateway_target: ip://127.0.0.1:20100
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "./override/collector.db", cfg.Database.Path)
	assert.Equal(t, "127.0.0.1:16012", cfg.Health.Addr)
	assert.Equal(t, "ip://127.0.0.1:30100", cfg.Storage.GatewayTarget)
}

func TestLoadRejectsLegacyStorageTargets(t *testing.T) {
	_, err := Load(writeCollectorConfig(t, `
storage:
  metadata_target: 127.0.0.1:20100
  access_target: 127.0.0.1:20102
`))
	require.Error(t, err)
}

func TestLoadRejectsMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read config")
}

func writeCollectorConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}
