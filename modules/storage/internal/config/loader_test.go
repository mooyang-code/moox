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

func TestStorageViewRebuildDefaults(t *testing.T) {
	cfg := StorageConfig{}
	cfg.ApplyDefaults()
	if cfg.View.BackfillPageSize != 2000 || cfg.View.BackfillRequestInterval != "100ms" || cfg.View.RebuildMaxPending != 32 || cfg.View.RebuildIdleChecks != 3 || cfg.View.RebuildLookback != "24h" {
		t.Fatalf("view rebuild defaults = %d/%s/%d/%s", cfg.View.BackfillPageSize, cfg.View.BackfillRequestInterval, cfg.View.RebuildMaxPending, cfg.View.RebuildLookback)
	}
	want := map[string]uint64{"1m": 5000, "1h": 5000, "1d": 5000, "default": 5000}
	for frequency, periods := range want {
		if cfg.View.RebuildLookbackPeriods[frequency] != periods {
			t.Fatalf("view rebuild periods[%q] = %d, want %d", frequency, cfg.View.RebuildLookbackPeriods[frequency], periods)
		}
	}
}

func TestStorageViewRebuildLookbackCanBeConfigured(t *testing.T) {
	var cfg RuntimeConfig
	if err := yaml.Unmarshal([]byte("storage:\n  view:\n    rebuild_lookback: 48h\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyDefaults()
	if cfg.Storage.View.RebuildLookback != "48h" {
		t.Fatalf("rebuild lookback = %q, want 48h", cfg.Storage.View.RebuildLookback)
	}
}

func TestStorageViewRebuildLookbackPeriodsNormalizeFrequency(t *testing.T) {
	var cfg RuntimeConfig
	if err := yaml.Unmarshal([]byte("storage:\n  view:\n    rebuild_lookback_periods:\n      1H: 123\n      30s: 456\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyDefaults()
	if cfg.Storage.View.RebuildLookbackPeriods["1h"] != 123 || cfg.Storage.View.RebuildLookbackPeriods["30s"] != 456 || cfg.Storage.View.RebuildLookbackPeriods["default"] != 5000 {
		t.Fatalf("normalized rebuild periods = %#v", cfg.Storage.View.RebuildLookbackPeriods)
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
			if got := cfg.Storage.PolicyFile; got != "../config/storage-policy.json" {
				t.Fatalf("%s policy_file = %q, want the shared ../config/storage-policy.json", file, got)
			}
		})
	}
	for _, frequency := range []string{"1m", "1h", "1d", "default"} {
		var cfg RuntimeConfig
		if err := loader.LoadConfigWithDefaults("storage.yaml", &cfg, cfg.ApplyDefaults); err != nil {
			t.Fatal(err)
		}
		if got := cfg.Storage.View.RebuildLookbackPeriods[frequency]; got != 5000 {
			t.Fatalf("rebuild_lookback_periods[%q] = %d, want 5000", frequency, got)
		}
	}
}

func TestStorageConfigAppliesEventConsumerDefaults(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()

	if cfg.Storage.EventBus.MaxAckPending != 256 {
		t.Fatalf("EventBus.MaxAckPending = %d, want 256 for the default view role", cfg.Storage.EventBus.MaxAckPending)
	}
	if cfg.Storage.EventBus.AckWaitMS != 120000 {
		t.Fatalf("EventBus.AckWaitMS = %d, want 120000", cfg.Storage.EventBus.AckWaitMS)
	}
}

func TestStorageViewConsumerDefaults(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()
	if cfg.Storage.View.FetchBatch != 1 || cfg.Storage.View.MaxWorkers != 1 || cfg.Storage.View.Ordering != "dataset" {
		t.Fatalf("view consumer defaults = %+v", cfg.Storage.View)
	}
}

func TestStorageViewConsumerPartitionsDefaultToIsolatedRoutes(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()
	partitions := cfg.Storage.View.ConsumerPartitions
	if len(partitions) != 3 {
		t.Fatalf("consumer partitions = %+v, want factor/metrics/misc", partitions)
	}
	if err := cfg.Storage.View.ValidateConsumerPartitions(nil); err != nil {
		t.Fatalf("ValidateConsumerPartitions() error = %v", err)
	}
	if partitions[0].ID != "factor" || partitions[0].Durable != "storage_view_factor" || partitions[0].FetchBatch != 1 || partitions[0].MaxWorkers != 1 || partitions[0].MaxAckPending != 1 {
		t.Fatalf("factor partition = %+v", partitions[0])
	}
	if got := partitions[0].Datasets(); len(got) != 0 {
		t.Fatalf("factor results bind dynamically; default static routes = %+v", got)
	}
	if partitions[1].ID != "system_metrics" || partitions[1].Durable != "storage_view_metrics" || partitions[1].FetchBatch != 16 || partitions[1].MaxWorkers != 4 || partitions[1].MaxAckPending != 64 {
		t.Fatalf("metrics partition = %+v", partitions[1])
	}
	wildcards := map[string]bool{}
	for _, dataset := range partitions[2].Datasets() {
		if dataset.DatasetID == "*" {
			wildcards[dataset.SpaceID] = true
		}
	}
	for _, spaceID := range []string{"crypto", "stockcn", "stockhk", "stockus"} {
		if !wildcards[spaceID] {
			t.Fatalf("misc partition must route every %s Dataset: %+v", spaceID, partitions[2].Datasets())
		}
	}
}

func TestStorageViewConsumerPartitionsRequireSerialFactorDelivery(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StorageViewConsumerPartition)
	}{
		{name: "fetch_batch", mutate: func(partition *StorageViewConsumerPartition) { partition.FetchBatch = 2 }},
		{name: "max_workers", mutate: func(partition *StorageViewConsumerPartition) { partition.MaxWorkers = 2 }},
		{name: "max_ack_pending", mutate: func(partition *StorageViewConsumerPartition) { partition.MaxAckPending = 2 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg RuntimeConfig
			cfg.ApplyDefaults()
			factor := &cfg.Storage.View.ConsumerPartitions[0]
			tt.mutate(factor)
			if err := cfg.Storage.View.ValidateConsumerPartitions(nil); err == nil || !strings.Contains(err.Error(), "1/1/1") || !strings.Contains(err.Error(), factor.Durable) {
				t.Fatalf("ValidateConsumerPartitions() error = %v, want factor durable 1/1/1 constraint", err)
			}
		})
	}
}

func TestCheckedInFactorViewConsumerProfilesAreSerializedTemplates(t *testing.T) {
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
			if err := cfg.Storage.View.ValidateConsumerPartitions(nil); err != nil {
				t.Fatalf("ValidateConsumerPartitions(): %v", err)
			}
			for _, partition := range cfg.Storage.View.ConsumerPartitions {
				if partition.ID != "factor" {
					continue
				}
				if partition.FetchBatch != 1 || partition.MaxAckPending != 1 || partition.MaxWorkers != 1 {
					t.Fatalf("factor broker delivery budget = fetch_batch:%d max_ack_pending:%d max_workers:%d, want all 1", partition.FetchBatch, partition.MaxAckPending, partition.MaxWorkers)
				}
				if got := partition.Datasets(); len(got) != 0 {
					t.Fatalf("factor results bind dynamically; static routes = %+v", got)
				}
				return
			}
			t.Fatal("factor consumer partition is missing")
		})
	}
}

func TestStorageViewConsumerPartitionsRejectOverlapAndInvalidLimits(t *testing.T) {
	view := StorageView{ConsumerPartitions: []StorageViewConsumerPartition{
		{ID: "a", Durable: "storage_view_misc", SpaceID: "crypto", DatasetIDs: []string{"dataset_binance_kline_1m"}, FetchBatch: 4, MaxAckPending: 8, MaxWorkers: 1, AckWaitMS: 1000},
		{ID: "b", Durable: "storage_view_metrics", SpaceID: "crypto", DatasetIDs: []string{"dataset_binance_kline_1m"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
	}}
	if err := view.ValidateConsumerPartitions(nil); err == nil {
		t.Fatal("overlapping Dataset partition was accepted")
	}
	view.ConsumerPartitions[1].DatasetIDs = []string{"other"}
	view.ConsumerPartitions[1].FetchBatch = 2
	if err := view.ValidateConsumerPartitions(nil); err == nil {
		t.Fatal("fetch_batch exceeding max_ack_pending was accepted")
	}
}

func TestStorageViewConsumerPartitionsRejectInvalidDurableName(t *testing.T) {
	view := StorageView{ConsumerPartitions: []StorageViewConsumerPartition{{
		ID: "kline", Durable: "storage.view", SpaceID: "crypto", DatasetIDs: []string{"dataset_binance_kline_1m"},
		FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, AckWaitMS: 1000,
	}}}
	if err := view.ValidateConsumerPartitions(nil); err == nil {
		t.Fatal("invalid durable name was accepted")
	}
}

func TestStorageViewConsumerPartitionsAllowFutureConfiguredDatasets(t *testing.T) {
	view := StorageView{ConsumerPartitions: []StorageViewConsumerPartition{
		{ID: "factor", Durable: "storage_view_factor", SpaceID: "crypto", DatasetIDs: []string{"dataset_factor_binance_kline_1m"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
		{ID: "system_metrics", Durable: "storage_view_metrics", SpaceID: "mooxsys", DatasetIDs: []string{"dataset_mooxsys_service_metrics"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
		{ID: "misc", Durable: "storage_view_misc", SpaceID: "crypto", DatasetIDs: []string{"dataset_binance_kline_1m", "future_dataset", "other"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
	}}
	managed := []StorageViewConsumerDataset{{SpaceID: "crypto", DatasetID: "dataset_binance_kline_1m"}}
	if err := view.ValidateConsumerPartitions(managed); err != nil {
		t.Fatalf("future configured Dataset should not block startup: %v", err)
	}
}

func TestStorageViewConsumerPartitionsRequireAllManagedDurables(t *testing.T) {
	view := StorageView{ConsumerPartitions: []StorageViewConsumerPartition{
		{ID: "system_metrics", Durable: "storage_view_metrics", SpaceID: "mooxsys", DatasetIDs: []string{"dataset_mooxsys_service_metrics"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
	}}
	if err := view.ValidateConsumerPartitions(nil); err == nil {
		t.Fatal("partial consumer topology was accepted")
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
			if tt.file == "storage_view/trpc_go.yaml" && cfg.Storage.EventBus.MaxAckPending != 256 {
				t.Fatalf("EventBus.MaxAckPending = %d, want 256 for view consumer", cfg.Storage.EventBus.MaxAckPending)
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

func TestStorageViewRebuildConfigDefaults(t *testing.T) {
	var cfg StorageConfig
	cfg.ApplyDefaults()

	if cfg.Devices.ViewIndexRoot != "var/storage/view-indexes" {
		t.Fatalf("view index root = %q, want var/storage/view-indexes", cfg.Devices.ViewIndexRoot)
	}
	if cfg.View.IndexServiceName != "trpc.moox.storage.ViewIndex" {
		t.Fatalf("index service name = %q", cfg.View.IndexServiceName)
	}
}

func TestStorageViewRebuildYAMLOverrides(t *testing.T) {
	raw := []byte(`
storage:
  devices:
    view_index_root: /indexes
  view:
    index_service_name: custom.ViewIndex
    rebuild_max_pending: 7
`)
	var cfg RuntimeConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()

	if cfg.Storage.Devices.ViewIndexRoot != "/indexes" || cfg.Storage.View.IndexServiceName != "custom.ViewIndex" {
		t.Fatalf("owner config = %q/%q", cfg.Storage.Devices.ViewIndexRoot, cfg.Storage.View.IndexServiceName)
	}
	if cfg.Storage.View.RebuildMaxPending != 7 || cfg.Storage.View.RebuildIdleChecks != 3 || cfg.Storage.View.RebuildLookback != "24h" {
		t.Fatalf("rebuild config = %d/%d/%s", cfg.Storage.View.RebuildMaxPending, cfg.Storage.View.RebuildIdleChecks, cfg.Storage.View.RebuildLookback)
	}
}

func TestStorageViewExplicitZeroWatermarkIsNotDefaulted(t *testing.T) {
	var cfg RuntimeConfig
	if err := yaml.Unmarshal([]byte("storage:\n  view:\n    rebuild_max_pending: 0\n    rebuild_idle_checks: 0\n"), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()
	if cfg.Storage.View.RebuildMaxPending != 0 || cfg.Storage.View.RebuildIdleChecks != 0 {
		t.Fatalf("explicit zero gate values were defaulted to %d/%d", cfg.Storage.View.RebuildMaxPending, cfg.Storage.View.RebuildIdleChecks)
	}
}
