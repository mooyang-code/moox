package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type loaderTestConfig struct {
	Name    string `yaml:"name"`
	Timeout int    `yaml:"timeout"`
}

func TestConfigLoaderLoadConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(configPath, []byte("name: storage\ntimeout: 30\n"), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	var cfg loaderTestConfig
	err := NewConfigLoader(dir).LoadConfig("app.yaml", &cfg)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Name != "storage" || cfg.Timeout != 30 {
		t.Fatalf("LoadConfig = %+v, want name=storage timeout=30", cfg)
	}
}

func TestConfigLoaderLoadConfigFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filename    string
		content     string
		wantErrText string
	}{
		{
			name:        "missing file",
			filename:    "missing.yaml",
			wantErrText: "读取配置文件失败",
		},
		{
			name:        "invalid yaml",
			filename:    "bad.yaml",
			content:     "name: [",
			wantErrText: "解析YAML失败",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tt.content != "" {
				path := filepath.Join(dir, tt.filename)
				if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
					t.Fatalf("write config failed: %v", err)
				}
			}

			var cfg loaderTestConfig
			err := NewConfigLoader(dir).LoadConfig(tt.filename, &cfg)
			if err == nil {
				t.Fatalf("LoadConfig returned nil error")
			}
			if !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("LoadConfig error = %q, want contains %q", err.Error(), tt.wantErrText)
			}
		})
	}
}

func TestConfigLoaderLoadConfigWithDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(configPath, []byte("name: storage\n"), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	var cfg loaderTestConfig
	defaultCalled := false
	err := NewConfigLoader(dir).LoadConfigWithDefaults("app.yaml", &cfg, func() {
		defaultCalled = true
		if cfg.Timeout == 0 {
			cfg.Timeout = 10
		}
	})
	if err != nil {
		t.Fatalf("LoadConfigWithDefaults returned error: %v", err)
	}
	if !defaultCalled {
		t.Fatalf("defaults function was not called")
	}
	if cfg.Timeout != 10 {
		t.Fatalf("Timeout = %d, want 10", cfg.Timeout)
	}
}

func TestStorageRuntimeConfigLoadsStorageSection(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "trpc_go.yaml")
	content := []byte(`
storage:
  root: ./var/storage
  metadata:
    path: ./var/storage/metadata/storage_metadata.db
    schema_path: ../../schema/storage_metadata.sql
  devices:
    pebble_path: ./var/storage/pebble
    duckdb_path: ./var/storage/duckdb/views.duckdb
    bleve_path: ./var/storage/bleve
    parquet_path: ./var/storage/archive
  changefeed:
    type: memory
    nats_url: ""
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	var cfg RuntimeConfig
	err := NewConfigLoader(dir).LoadConfigWithDefaults("trpc_go.yaml", &cfg, cfg.ApplyDefaults)
	if err != nil {
		t.Fatalf("LoadConfigWithDefaults returned error: %v", err)
	}
	if cfg.Storage.Root != "./var/storage" {
		t.Fatalf("Storage.Root = %q", cfg.Storage.Root)
	}
	if cfg.Storage.Metadata.Path != "./var/storage/metadata/storage_metadata.db" {
		t.Fatalf("metadata path = %q", cfg.Storage.Metadata.Path)
	}
	if cfg.Storage.Devices.DuckDBPath != "./var/storage/duckdb/views.duckdb" {
		t.Fatalf("duckdb path = %q", cfg.Storage.Devices.DuckDBPath)
	}
	if cfg.Storage.Changefeed.Type != "memory" {
		t.Fatalf("changefeed type = %q", cfg.Storage.Changefeed.Type)
	}
}
