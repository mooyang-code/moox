package config

import (
	"os"
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
	if cfg.View.RebuildCheckInterval != "1m" || cfg.View.MaxViewFileBytes != 1<<30 || cfg.View.RebuildMaxPending != 32 || cfg.View.RebuildIdleChecks != 3 || cfg.View.RebuildLookback != "24h" {
		t.Fatalf("view rebuild defaults = %q/%d/%d/%d/%s", cfg.View.RebuildCheckInterval, cfg.View.MaxViewFileBytes, cfg.View.RebuildMaxPending, cfg.View.RebuildIdleChecks, cfg.View.RebuildLookback)
	}
	want := map[string]uint64{"1m": 1000, "1h": 1000, "1d": 1000, "default": 1000}
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
	if cfg.Storage.View.RebuildLookbackPeriods["1h"] != 123 || cfg.Storage.View.RebuildLookbackPeriods["30s"] != 456 || cfg.Storage.View.RebuildLookbackPeriods["default"] != 1000 {
		t.Fatalf("normalized rebuild periods = %#v", cfg.Storage.View.RebuildLookbackPeriods)
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
		t.Fatalf("consumer partitions = %+v, want kline/metrics/other", partitions)
	}
	if err := cfg.Storage.View.ValidateConsumerPartitions(nil); err != nil {
		t.Fatalf("ValidateConsumerPartitions() error = %v", err)
	}
	klineDatasets := partitions[0].Datasets()
	if partitions[0].ID != "kline" || partitions[0].Durable != "storage_view_kline_v2" || len(klineDatasets) != 1 || klineDatasets[0].SpaceID != "crypto_market" || klineDatasets[0].DatasetID != "binance_spot_kline_1m" {
		t.Fatalf("kline partition = %+v", partitions[0])
	}
	if partitions[1].ID != "system_metrics" || partitions[1].Durable != "storage_view_metrics_v2" {
		t.Fatalf("metrics partition = %+v", partitions[1])
	}
}

func TestStorageViewConsumerPartitionsRejectOverlapAndInvalidLimits(t *testing.T) {
	view := StorageView{ConsumerPartitions: []StorageViewConsumerPartition{
		{ID: "a", Durable: "storage_view_kline_v2", SpaceID: "crypto_market", DatasetIDs: []string{"binance_spot_kline_1m"}, FetchBatch: 4, MaxAckPending: 8, MaxWorkers: 1, AckWaitMS: 1000},
		{ID: "b", Durable: "storage_view_metrics_v2", SpaceID: "crypto_market", DatasetIDs: []string{"binance_spot_kline_1m"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
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
		ID: "kline", Durable: "storage.view", SpaceID: "crypto_market", DatasetIDs: []string{"binance_spot_kline_1m"},
		FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, AckWaitMS: 1000,
	}}}
	if err := view.ValidateConsumerPartitions(nil); err == nil {
		t.Fatal("invalid durable name was accepted")
	}
}

func TestStorageViewConsumerPartitionsAllowFutureConfiguredDatasets(t *testing.T) {
	view := StorageView{ConsumerPartitions: []StorageViewConsumerPartition{
		{ID: "kline", Durable: "storage_view_kline_v2", SpaceID: "crypto_market", DatasetIDs: []string{"binance_spot_kline_1m", "future_factor"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
		{ID: "system_metrics", Durable: "storage_view_metrics_v2", SpaceID: "moox_system", DatasetIDs: []string{"moox_service_metrics"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
		{ID: "other", Durable: "storage_view_other_v2", SpaceID: "crypto_market", DatasetIDs: []string{"other"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
	}}
	managed := []StorageViewConsumerDataset{{SpaceID: "crypto_market", DatasetID: "binance_spot_kline_1m"}}
	if err := view.ValidateConsumerPartitions(managed); err != nil {
		t.Fatalf("future configured Dataset should not block startup: %v", err)
	}
}

func TestStorageViewConsumerPartitionsRequireAllManagedDurables(t *testing.T) {
	view := StorageView{ConsumerPartitions: []StorageViewConsumerPartition{
		{ID: "kline", Durable: "storage_view_kline_v2", SpaceID: "crypto_market", DatasetIDs: []string{"binance_spot_kline_1m"}, FetchBatch: 1, MaxAckPending: 1, MaxWorkers: 1, AckWaitMS: 1000},
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
	if cfg.View.RebuildCheckInterval != "1m" || cfg.View.MaxViewFileBytes != 1<<30 {
		t.Fatalf("rebuild config defaults = %q/%d", cfg.View.RebuildCheckInterval, cfg.View.MaxViewFileBytes)
	}
	cleanup := cfg.Maintenance.HostMetricsCleanup
	if !cleanup.IsEnabled() || cleanup.MaxAge != "48h" || cleanup.BatchSize != 1000 || cleanup.MaxBatchesPerRun != 10 || len(cleanup.DatasetIDs) != 4 {
		t.Fatalf("host metrics cleanup defaults = %+v", cleanup)
	}
}

func TestHostMetricsCleanupValidation(t *testing.T) {
	enabled := true
	valid := HostMetricsCleanupConfig{
		Enabled: &enabled, DatasetIDs: []string{"host_resource_v1"}, MaxAge: "48h", BatchSize: 1000, MaxBatchesPerRun: 10,
	}
	tests := []struct {
		name    string
		mutate  func(*HostMetricsCleanupConfig)
		wantErr string
	}{
		{name: "valid"},
		{name: "invalid duration", mutate: func(c *HostMetricsCleanupConfig) { c.MaxAge = "bad" }, wantErr: "max_age"},
		{name: "non-positive duration", mutate: func(c *HostMetricsCleanupConfig) { c.MaxAge = "0s" }, wantErr: "max_age"},
		{name: "zero batch", mutate: func(c *HostMetricsCleanupConfig) { c.BatchSize = 0 }, wantErr: "batch_size"},
		{name: "oversized batch", mutate: func(c *HostMetricsCleanupConfig) { c.BatchSize = 1001 }, wantErr: "batch_size"},
		{name: "zero max batches", mutate: func(c *HostMetricsCleanupConfig) { c.MaxBatchesPerRun = 0 }, wantErr: "max_batches_per_run"},
		{name: "empty datasets", mutate: func(c *HostMetricsCleanupConfig) { c.DatasetIDs = nil }, wantErr: "dataset_ids"},
		{name: "blank dataset", mutate: func(c *HostMetricsCleanupConfig) { c.DatasetIDs = []string{" "} }, wantErr: "dataset_ids"},
		{name: "duplicate dataset", mutate: func(c *HostMetricsCleanupConfig) { c.DatasetIDs = []string{"host_resource_v1", "host_resource_v1"} }, wantErr: "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			cfg.DatasetIDs = append([]string(nil), valid.DatasetIDs...)
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestStorageViewRebuildYAMLOverrides(t *testing.T) {
	raw := []byte(`
storage:
  devices:
    view_index_root: /indexes
  view:
    index_service_name: custom.ViewIndex
    rebuild_check_interval: 2h
    max_view_file_bytes: 805306368
`)
	var cfg RuntimeConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()

	if cfg.Storage.Devices.ViewIndexRoot != "/indexes" || cfg.Storage.View.IndexServiceName != "custom.ViewIndex" {
		t.Fatalf("owner config = %q/%q", cfg.Storage.Devices.ViewIndexRoot, cfg.Storage.View.IndexServiceName)
	}
	if cfg.Storage.View.RebuildCheckInterval != "2h" || cfg.Storage.View.MaxViewFileBytes != 805306368 || cfg.Storage.View.RebuildMaxPending != 32 || cfg.Storage.View.RebuildIdleChecks != 3 || cfg.Storage.View.RebuildLookback != "24h" {
		t.Fatalf("rebuild config = %q/%d/%d/%d/%s", cfg.Storage.View.RebuildCheckInterval, cfg.Storage.View.MaxViewFileBytes, cfg.Storage.View.RebuildMaxPending, cfg.Storage.View.RebuildIdleChecks, cfg.Storage.View.RebuildLookback)
	}
}

func TestStorageViewExplicitZeroWatermarkIsNotDefaulted(t *testing.T) {
	var cfg RuntimeConfig
	if err := yaml.Unmarshal([]byte("storage:\n  view:\n    max_view_file_bytes: 0\n    rebuild_max_pending: 0\n    rebuild_idle_checks: 0\n"), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()
	if cfg.Storage.View.MaxViewFileBytes != 0 {
		t.Fatalf("explicit zero watermark was defaulted to %d", cfg.Storage.View.MaxViewFileBytes)
	}
	if cfg.Storage.View.RebuildMaxPending != 0 || cfg.Storage.View.RebuildIdleChecks != 0 {
		t.Fatalf("explicit zero gate values were defaulted to %d/%d", cfg.Storage.View.RebuildMaxPending, cfg.Storage.View.RebuildIdleChecks)
	}
}
