package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

func TestStorageConfigAppliesHealthDefault(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()

	if cfg.Storage.Health.Addr != ":20210" {
		t.Fatalf("Storage.Health.Addr = %q, want :20210", cfg.Storage.Health.Addr)
	}
}

func TestStorageConfigRolesAreExplicit(t *testing.T) {
	cfg := StorageConfig{Roles: []string{"primary", "view"}}
	if !cfg.HasRole("primary") || !cfg.HasRole("view") || cfg.HasRole("view_index") {
		t.Fatal("role matching must be explicit")
	}
}

func TestStorageViewBackfillDefaults(t *testing.T) {
	cfg := StorageConfig{}
	cfg.ApplyDefaults()
	if cfg.View.BackfillPageSize != 2000 || cfg.View.BackfillRequestInterval != "100ms" {
		t.Fatalf("view backfill defaults = %d/%s", cfg.View.BackfillPageSize, cfg.View.BackfillRequestInterval)
	}
}

func TestCheckedInStorageRoleConfigsShareThePolicyFile(t *testing.T) {
	loader := NewConfigLoader("../../config")
	for _, file := range []string{"storage.yaml", "storage.primary.yaml", "storage_view/trpc_go.yaml"} {
		t.Run(file, func(t *testing.T) {
			var cfg RuntimeConfig
			if err := loader.LoadConfigWithDefaults(file, &cfg, cfg.ApplyDefaults); err != nil {
				t.Fatalf("LoadConfigWithDefaults(%s): %v", file, err)
			}
			if got := cfg.Storage.PolicyFile; got != "config/storage-policy.json" {
				t.Fatalf("%s policy_file = %q, want the component-owned config/storage-policy.json", file, got)
			}
		})
	}
}

// View 只有一种消费方式：全局并发参数默认都是 1（同一数据集内按顺序处理）。
func TestStorageViewConsumerDefaults(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()
	view := cfg.Storage.View
	if view.FetchBatch != 1 || view.MaxWorkers != 1 || view.MaxAckPending != 1 || view.AckWaitMS != StorageViewAckWaitMS {
		t.Fatalf("view consumer defaults = %+v", view)
	}
	if err := view.ValidateConsumer(); err != nil {
		t.Fatalf("default consumer settings: %v", err)
	}
}

func TestStorageViewMaxAckPendingFollowsFetchBatchWhenOmitted(t *testing.T) {
	var cfg RuntimeConfig
	cfg.Storage.View.FetchBatch = 4
	cfg.ApplyDefaults()
	if cfg.Storage.View.MaxAckPending != 4 {
		t.Fatalf("max_ack_pending = %d, want fetch_batch 4", cfg.Storage.View.MaxAckPending)
	}
	if err := cfg.Storage.View.ValidateConsumer(); err != nil {
		t.Fatal(err)
	}
}

func TestStorageViewConsumerRejectsInvalidLimits(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()
	view := cfg.Storage.View
	view.FetchBatch, view.MaxAckPending = 8, 4
	if err := view.ValidateConsumer(); err == nil {
		t.Fatal("fetch_batch exceeding max_ack_pending was accepted")
	}
	view = cfg.Storage.View
	view.MaxWorkers = 0
	if err := view.ValidateConsumer(); err == nil {
		t.Fatal("non-positive max_workers was accepted")
	}
}

// 仓库里的两份配置（Storage 主机与 View 组件）都使用全局默认的串行消费。
func TestCheckedInViewConsumerProfilesAreSerial(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "config", "storage.yaml"),
		filepath.Join("..", "..", "config", "storage_view", "trpc_go.yaml"),
	} {
		t.Run(path, func(t *testing.T) {
			var cfg RuntimeConfig
			loader := NewConfigLoader(filepath.Dir(path))
			if err := loader.LoadConfigWithDefaults(filepath.Base(path), &cfg, cfg.ApplyDefaults); err != nil {
				t.Fatalf("LoadConfigWithDefaults(%s): %v", path, err)
			}
			view := cfg.Storage.View
			if err := view.ValidateConsumer(); err != nil {
				t.Fatalf("ValidateConsumer(): %v", err)
			}
			if view.FetchBatch != 1 || view.MaxAckPending != 1 || view.MaxWorkers != 1 {
				t.Fatalf("delivery budget = fetch_batch:%d max_ack_pending:%d max_workers:%d, want all 1", view.FetchBatch, view.MaxAckPending, view.MaxWorkers)
			}
		})
	}
}

func TestStorageConfigApplyHomeRootRebasesDefaultPaths(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()

	cfg.Storage.ApplyHomeRoot("/data/moox/storage")

	if cfg.Storage.Root != "/data/moox/storage" {
		t.Fatalf("Root = %q, want /data/moox/storage", cfg.Storage.Root)
	}
	if cfg.Storage.Metadata.Path != "/data/moox/storage/metadata/storage_metadata.db" {
		t.Fatalf("Metadata.Path = %q", cfg.Storage.Metadata.Path)
	}
	if cfg.Storage.Devices.ViewIndexRoot != "/data/moox/storage/view-indexes" {
		t.Fatalf("Devices.ViewIndexRoot = %q", cfg.Storage.Devices.ViewIndexRoot)
	}
	if cfg.Storage.Devices.PebblePath != "/data/moox/storage/pebble" {
		t.Fatalf("Devices.PebblePath = %q", cfg.Storage.Devices.PebblePath)
	}
}

func TestStorageConfigApplyHomeRootKeepsAbsoluteCustomPaths(t *testing.T) {
	cfg := StorageConfig{
		Root: "./var/storage",
		Metadata: StorageMetadata{
			Path: "/custom/metadata.db",
		},
		Devices: StorageDevices{
			ViewIndexRoot: "/custom/view-indexes",
		},
	}

	cfg.ApplyHomeRoot("/data/moox/storage")

	if cfg.Metadata.Path != "/custom/metadata.db" {
		t.Fatalf("Metadata.Path = %q, want /custom/metadata.db", cfg.Metadata.Path)
	}
	if cfg.Devices.ViewIndexRoot != "/custom/view-indexes" {
		t.Fatalf("Devices.ViewIndexRoot = %q, want /custom/view-indexes", cfg.Devices.ViewIndexRoot)
	}
}

func TestStorageConfigApplyHomeRootRebasesAbsolutePathsUnderOldRoot(t *testing.T) {
	cfg := StorageConfig{
		Root: "/old/storage",
		Metadata: StorageMetadata{
			Path: "/old/storage/metadata/storage_metadata.db",
		},
		Devices: StorageDevices{
			PebblePath:    "/old/storage/pebble",
			ViewIndexRoot: "/old/storage/view-indexes",
		},
	}

	cfg.ApplyHomeRoot("/new/storage")

	if cfg.Metadata.Path != "/new/storage/metadata/storage_metadata.db" {
		t.Fatalf("Metadata.Path = %q", cfg.Metadata.Path)
	}
	if cfg.Devices.ViewIndexRoot != "/new/storage/view-indexes" {
		t.Fatalf("Devices.ViewIndexRoot = %q", cfg.Devices.ViewIndexRoot)
	}
	if cfg.Devices.PebblePath != "/new/storage/pebble" {
		t.Fatalf("Devices.PebblePath = %q", cfg.Devices.PebblePath)
	}
}

func TestStorageDeploymentConfigFilesLoadRolesAndHealth(t *testing.T) {
	tests := []struct {
		file       string
		wantRole   string
		wantHealth string
	}{
		{file: "storage.primary.yaml", wantRole: "primary", wantHealth: ":20210"},
		{file: "storage_view/trpc_go.yaml", wantRole: "view", wantHealth: ":20211"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			var cfg RuntimeConfig
			if err := NewConfigLoader("../../config").LoadConfigWithDefaults(tt.file, &cfg, cfg.ApplyDefaults); err != nil {
				t.Fatalf("LoadConfigWithDefaults(%s): %v", tt.file, err)
			}
			if !cfg.Storage.HasRole(tt.wantRole) {
				t.Fatalf("roles = %v, want %s", cfg.Storage.Roles, tt.wantRole)
			}
			if cfg.Storage.EventBus.CredentialFile != "~/.config/moox/eventbus/storage-eventbus.yaml" {
				t.Fatalf("EventBus.CredentialFile = %q", cfg.Storage.EventBus.CredentialFile)
			}
			if cfg.Storage.Health.Addr != tt.wantHealth {
				t.Fatalf("Health.Addr = %q, want %q", cfg.Storage.Health.Addr, tt.wantHealth)
			}
		})
	}
}

func TestStorageDeploymentEventBusConfigOwnsOnlyConnectionAndDeliverySettings(t *testing.T) {
	allowed := map[string]bool{
		"credential_file": true,
		"consumer":        true,
		"max_ack_pending": true,
		"ack_wait_ms":     true,
	}
	files := []string{"storage.yaml", "storage.primary.yaml", "storage.node.yaml", "storage_view/trpc_go.yaml"}
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile("../../config/" + file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			var doc struct {
				Storage struct {
					EventBus map[string]interface{} `yaml:"eventbus"`
				} `yaml:"storage"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("unmarshal %s: %v", file, err)
			}
			for key := range doc.Storage.EventBus {
				if !allowed[key] {
					t.Fatalf("%s storage.eventbus still exposes %q", file, key)
				}
			}
		})
	}
}

func TestStorageConfigLoaderRejectsRemovedEventBusFields(t *testing.T) {
	path := t.TempDir()
	if err := os.WriteFile(path+"/storage.yaml", []byte("storage:\n  eventbus:\n    stream_name: MOOX_STORAGE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var cfg RuntimeConfig
	err := NewConfigLoader(path).LoadConfigWithDefaults("storage.yaml", &cfg, cfg.ApplyDefaults)
	if err == nil || !strings.Contains(err.Error(), "stream_name") {
		t.Fatalf("LoadConfigWithDefaults() error = %v, want removed stream_name rejection", err)
	}
}

func TestStorageConfigsContainNoLegacyPathsOrRotationAndDependentsDoNotOwnIndexRoot(t *testing.T) {
	files := []string{"storage.yaml", "storage.primary.yaml", "storage_view/trpc_go.yaml"}
	for _, file := range files {
		raw, err := os.ReadFile("../../config/" + file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(raw)
		for _, legacy := range []string{"duckdb_" + "path:", "bleve_" + "path:", "rota" + "tion:"} {
			if strings.Contains(text, legacy) {
				t.Fatalf("%s still contains legacy key %q", file, legacy)
			}
		}
	}
}

func TestRuntimeGatewayDefaultsAndRemovedStorageRPC(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()
	if cfg.GatewayClient.Caller != "storage-view" || cfg.GatewayClient.KeyFile != "../../secrets/caller-storage-view.key" {
		t.Fatal("invalid storage-view gateway defaults")
	}
	for _, raw := range []string{
		"storage:\n  view:\n    storage_rpc:\n      gateway_target: legacy\n",
		"gateway_client:\n  caller: storage-view\n  target: legacy\n",
		"gateway_client:\n  caller: storage-view\n  key_id: first\n  key_id: second\n",
		"storage:\n  view:\n    metadata_service_name: legacy\n",
		"storage:\n  view:\n    primary_store_service_name: legacy\n",
		"storage:\n  view:\n    index_service_name: legacy\n",
	} {
		path := filepath.Join(t.TempDir(), "app.yaml")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		var loaded RuntimeConfig
		if err := NewConfigLoader(filepath.Dir(path)).LoadConfig(filepath.Base(path), &loaded); err == nil {
			t.Fatal("accepted removed gateway configuration")
		}
	}
}
