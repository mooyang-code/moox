package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loaderTestConfig 是配置加载测试使用的临时配置结构。
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

func TestStorageRuntimeConfigLoadsStorageBusinessConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "storage.yaml")
	content := []byte(`
storage:
  root: ./var/storage
  metadata:
    path: ./var/storage/metadata/storage_metadata.db
  devices:
    pebble_path: ./var/storage/pebble
    duckdb_path: ./var/storage/duckdb/views.duckdb
    bleve_path: ./var/storage/bleve
    parquet_path: ./var/storage/archive
  eventbus:
    type: memory
    nats_url: ""
    consumer_name: storage_rows_custom
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	var cfg RuntimeConfig
	err := NewConfigLoader(dir).LoadConfigWithDefaults("storage.yaml", &cfg, cfg.ApplyDefaults)
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
	if cfg.Storage.EventBus.Type != "memory" {
		t.Fatalf("eventbus type = %q", cfg.Storage.EventBus.Type)
	}
	if cfg.Storage.EventBus.ConsumerName != "storage_rows_custom" {
		t.Fatalf("eventbus consumer_name = %q", cfg.Storage.EventBus.ConsumerName)
	}
}

func TestStorageRuntimeConfigDefaultsRolesEventBusAndDeriver(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "storage.yaml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	var cfg RuntimeConfig
	err := NewConfigLoader(dir).LoadConfigWithDefaults("storage.yaml", &cfg, cfg.ApplyDefaults)
	if err != nil {
		t.Fatalf("LoadConfigWithDefaults returned error: %v", err)
	}
	if roles := strings.Join(cfg.Storage.Roles, ","); roles != "access,deriver" {
		t.Fatalf("storage roles = %q, want access,deriver", roles)
	}
	if cfg.Storage.EventBus.Type != "nats" {
		t.Fatalf("eventbus type = %q", cfg.Storage.EventBus.Type)
	}
	if cfg.Storage.EventBus.NATSURL != "nats://127.0.0.1:4222" {
		t.Fatalf("eventbus nats_url = %q", cfg.Storage.EventBus.NATSURL)
	}
	if cfg.Storage.EventBus.StreamName != "MOOX_STORAGE" {
		t.Fatalf("eventbus stream_name = %q", cfg.Storage.EventBus.StreamName)
	}
	if cfg.Storage.EventBus.SubjectPrefix != "moox.storage" {
		t.Fatalf("eventbus subject_prefix = %q", cfg.Storage.EventBus.SubjectPrefix)
	}
	if cfg.Storage.EventBus.ConsumerName != "storage_deriver" {
		t.Fatalf("eventbus consumer_name = %q", cfg.Storage.EventBus.ConsumerName)
	}
	if cfg.Storage.Deriver.AccessServiceName != "trpc.storage.access.AccessService" {
		t.Fatalf("deriver access_service_name = %q", cfg.Storage.Deriver.AccessServiceName)
	}
	if cfg.Storage.Deriver.BatchSize != 500 {
		t.Fatalf("deriver batch_size = %d", cfg.Storage.Deriver.BatchSize)
	}
	if cfg.Storage.Deriver.BatchWaitMS != 200 {
		t.Fatalf("deriver batch_wait_ms = %d", cfg.Storage.Deriver.BatchWaitMS)
	}
	if cfg.Storage.Deriver.MaxWorkers != 4 {
		t.Fatalf("deriver max_workers = %d", cfg.Storage.Deriver.MaxWorkers)
	}
}

func TestStorageRuntimeConfigHasRole(t *testing.T) {
	t.Parallel()

	cfg := StorageConfig{Roles: []string{" Access ", "DERIVER"}}
	if !cfg.HasRole("access") {
		t.Fatalf("HasRole(access) = false, want true")
	}
	if !cfg.HasRole("deriver") {
		t.Fatalf("HasRole(deriver) = false, want true")
	}
	if cfg.HasRole("primary") {
		t.Fatalf("HasRole(primary) = true, want false")
	}
}

func TestStorageRuntimeConfigDefaultsNATSConsumerName(t *testing.T) {
	t.Parallel()

	var cfg RuntimeConfig
	cfg.Storage.EventBus.Type = "nats"
	cfg.ApplyDefaults()
	if cfg.Storage.EventBus.ConsumerName != "storage_deriver" {
		t.Fatalf("eventbus consumer_name = %q", cfg.Storage.EventBus.ConsumerName)
	}
}

func TestStorageRuntimeConfigDefaultsEventSubjectPrefix(t *testing.T) {
	t.Parallel()

	var cfg RuntimeConfig
	cfg.ApplyDefaults()
	if cfg.Storage.EventBus.SubjectPrefix != "moox.storage" {
		t.Fatalf("eventbus subject_prefix = %q", cfg.Storage.EventBus.SubjectPrefix)
	}
}

func TestStorageRuntimeConfigDefaultsPrimaryServiceNameToLocal(t *testing.T) {
	t.Parallel()

	var cfg RuntimeConfig
	cfg.ApplyDefaults()
	if cfg.Storage.Primary.ServiceName != "" {
		t.Fatalf("primary service_name = %q, want empty for local primary", cfg.Storage.Primary.ServiceName)
	}
}

func TestStorageRuntimeConfigKeepsCustomSubjectPrefix(t *testing.T) {
	t.Parallel()

	var cfg RuntimeConfig
	cfg.Storage.EventBus.SubjectPrefix = "custom.storage"
	cfg.ApplyDefaults()
	if cfg.Storage.EventBus.SubjectPrefix != "custom.storage" {
		t.Fatalf("eventbus subject_prefix = %q", cfg.Storage.EventBus.SubjectPrefix)
	}
}
