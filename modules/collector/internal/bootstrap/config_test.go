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
	t.Setenv("MOOX_COLLECTOR_STORAGE_METADATA_TARGET", "127.0.0.1:30100")
	t.Setenv("MOOX_COLLECTOR_STORAGE_ACCESS_TARGET", "127.0.0.1:30102")

	path := writeCollectorConfig(t, `
database:
  path: ./original/collector.db
storage:
  metadata_target: 127.0.0.1:20100
  access_target: 127.0.0.1:20102
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "./override/collector.db", cfg.Database.Path)
	assert.Equal(t, "127.0.0.1:16012", cfg.Health.Addr)
	assert.Equal(t, "127.0.0.1:30100", cfg.Storage.MetadataTarget)
	assert.Equal(t, "127.0.0.1:30102", cfg.Storage.PrimaryTarget)
}

func TestLoadRejectsHTTPStorageTargets(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "metadata",
			yaml: `
storage:
  metadata_target: http://127.0.0.1:20100
  access_target: 127.0.0.1:20102
`,
		},
		{
			name: "access",
			yaml: `
storage:
  metadata_target: 127.0.0.1:20100
  access_target: https://127.0.0.1:20102
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeCollectorConfig(t, tt.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "storage.")
		})
	}
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
