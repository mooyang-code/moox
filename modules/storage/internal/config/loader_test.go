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

func TestStorageRuntimeConfigDefaultsRolesEventBusAndView(t *testing.T) {
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
	if roles := strings.Join(cfg.Storage.Roles, ","); roles != "access,view" {
		t.Fatalf("storage roles = %q, want access,view", roles)
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
	if cfg.Storage.EventBus.ConsumerName != "storage_view" {
		t.Fatalf("eventbus consumer_name = %q", cfg.Storage.EventBus.ConsumerName)
	}
	if cfg.Storage.EventBus.Embedded.Enabled {
		t.Fatalf("embedded nats should be opt-in by default")
	}
	if cfg.Storage.View.MetadataServiceName != "trpc.moox.storage.Metadata" {
		t.Fatalf("view metadata_service_name = %q", cfg.Storage.View.MetadataServiceName)
	}
	if cfg.Storage.View.AccessServiceName != "trpc.moox.storage.Access" {
		t.Fatalf("view access_service_name = %q", cfg.Storage.View.AccessServiceName)
	}
	if cfg.Storage.View.BatchSize != 500 {
		t.Fatalf("view batch_size = %d", cfg.Storage.View.BatchSize)
	}
	if cfg.Storage.View.BatchWaitMS != 200 {
		t.Fatalf("view batch_wait_ms = %d", cfg.Storage.View.BatchWaitMS)
	}
	if cfg.Storage.View.MaxWorkers != 4 {
		t.Fatalf("view max_workers = %d", cfg.Storage.View.MaxWorkers)
	}
}

func TestStorageRuntimeConfigDefaultsEmbeddedNATS(t *testing.T) {
	t.Parallel()

	var cfg RuntimeConfig
	cfg.Storage.Root = "/tmp/moox-storage"
	cfg.Storage.EventBus.Type = "nats"
	cfg.Storage.EventBus.Embedded.Enabled = true

	cfg.ApplyDefaults()

	if !cfg.Storage.EventBus.Embedded.Enabled {
		t.Fatalf("embedded nats should stay enabled")
	}
	if cfg.Storage.EventBus.Embedded.Host != "127.0.0.1" {
		t.Fatalf("embedded nats host = %q", cfg.Storage.EventBus.Embedded.Host)
	}
	if cfg.Storage.EventBus.Embedded.Port != 4222 {
		t.Fatalf("embedded nats port = %d", cfg.Storage.EventBus.Embedded.Port)
	}
	if cfg.Storage.EventBus.Embedded.StoreDir != filepath.Join("/tmp/moox-storage", "nats") {
		t.Fatalf("embedded nats store_dir = %q", cfg.Storage.EventBus.Embedded.StoreDir)
	}
	if cfg.Storage.EventBus.Embedded.StartupTimeoutMS != 10000 {
		t.Fatalf("embedded nats startup_timeout_ms = %d", cfg.Storage.EventBus.Embedded.StartupTimeoutMS)
	}
}

func TestStorageRuntimeConfigNormalizesNonPositiveViewBatchSettings(t *testing.T) {
	t.Parallel()

	cfg := RuntimeConfig{}
	cfg.Storage.View.BatchSize = -1
	cfg.Storage.View.BatchWaitMS = -10
	cfg.Storage.View.MaxWorkers = -2

	cfg.ApplyDefaults()

	if cfg.Storage.View.BatchSize != 500 {
		t.Fatalf("view batch_size = %d, want 500", cfg.Storage.View.BatchSize)
	}
	if cfg.Storage.View.BatchWaitMS != 200 {
		t.Fatalf("view batch_wait_ms = %d, want 200", cfg.Storage.View.BatchWaitMS)
	}
	if cfg.Storage.View.MaxWorkers != 4 {
		t.Fatalf("view max_workers = %d, want 4", cfg.Storage.View.MaxWorkers)
	}
}

func TestStorageRuntimeConfigDefaultsViewAccessServiceNameForMemoryEventBus(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "storage.yaml")
	content := []byte(`
storage:
  eventbus:
    type: memory
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	var cfg RuntimeConfig
	err := NewConfigLoader(dir).LoadConfigWithDefaults("storage.yaml", &cfg, cfg.ApplyDefaults)
	if err != nil {
		t.Fatalf("LoadConfigWithDefaults returned error: %v", err)
	}
	if cfg.Storage.EventBus.Type != "memory" {
		t.Fatalf("eventbus type = %q", cfg.Storage.EventBus.Type)
	}
	if cfg.Storage.View.MetadataServiceName != "trpc.moox.storage.Metadata" {
		t.Fatalf("view metadata_service_name = %q", cfg.Storage.View.MetadataServiceName)
	}
	if cfg.Storage.View.AccessServiceName != "trpc.moox.storage.Access" {
		t.Fatalf("view access_service_name = %q", cfg.Storage.View.AccessServiceName)
	}
}

func TestStorageRuntimeConfigHasRole(t *testing.T) {
	t.Parallel()

	cfg := StorageConfig{Roles: []string{" Access ", "VIEW"}}
	if !cfg.HasRole("access") {
		t.Fatalf("HasRole(access) = false, want true")
	}
	if !cfg.HasRole("view") {
		t.Fatalf("HasRole(view) = false, want true")
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
	if cfg.Storage.EventBus.ConsumerName != "storage_view" {
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
