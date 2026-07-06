package control

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultFactorConfig(t *testing.T) {
	cfg := Default()

	if cfg.Storage.MetadataTarget != "127.0.0.1:20100" {
		t.Fatalf("metadata target = %q", cfg.Storage.MetadataTarget)
	}
	if cfg.Storage.AccessTarget != "127.0.0.1:20102" {
		t.Fatalf("access target = %q", cfg.Storage.AccessTarget)
	}
	if cfg.NATS.Stream != "MOOX_STORAGE" {
		t.Fatalf("nats stream = %q", cfg.NATS.Stream)
	}
	if cfg.Engine.Workers <= 0 {
		t.Fatalf("engine workers = %d, want > 0", cfg.Engine.Workers)
	}
}

func TestLoadAppliesFactorEnvOverrides(t *testing.T) {
	t.Setenv("MOOX_FACTOR_DB_PATH", "./override/factor.db")
	t.Setenv("MOOX_FACTOR_NATS_URL", "")
	t.Setenv("MOOX_FACTOR_ENGINE_PYTHON_BIN", "/tmp/factor-python")

	path := writeConfig(t, `
database:
  path: ./original/factor.db
nats:
  url: nats://127.0.0.1:4222
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.Path != "./override/factor.db" {
		t.Fatalf("database path = %q", cfg.Database.Path)
	}
	if cfg.NATS.URL != "" {
		t.Fatalf("nats url = %q, want empty override", cfg.NATS.URL)
	}
	if cfg.Engine.PythonBin != "/tmp/factor-python" {
		t.Fatalf("python bin = %q", cfg.Engine.PythonBin)
	}
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
			_, err := Load(writeConfig(t, tt.yaml))
			if err == nil {
				t.Fatal("Load() error = nil, want invalid storage target")
			}
		})
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
