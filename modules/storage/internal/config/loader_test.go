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

func TestStorageConfigAllRoleMatchesConcreteRoles(t *testing.T) {
	cfg := StorageConfig{Roles: []string{"all"}}

	for _, role := range []string{"access", "view", "view_builder", "view_query", "view_index", "archive", "primary"} {
		if !cfg.HasRole(role) {
			t.Fatalf("HasRole(%q) = false, want true for all role", role)
		}
	}
}

func TestStorageViewMaintenanceTargetStaysBelowCustomMaximum(t *testing.T) {
	cfg := StorageConfig{View: StorageView{Maintenance: StorageViewMaintenance{MaxEntries: 100}}}
	cfg.ApplyDefaults()

	if cfg.View.Maintenance.TargetEntries != 75 {
		t.Fatalf("TargetEntries = %d, want 75", cfg.View.Maintenance.TargetEntries)
	}
}

func TestStorageConfigAppliesEventBusOOMGuardDefaults(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()

	if cfg.Storage.EventBus.MaxAgeHours != 24 {
		t.Fatalf("EventBus.MaxAgeHours = %d, want 24", cfg.Storage.EventBus.MaxAgeHours)
	}
	if cfg.Storage.EventBus.MaxMsgs != 500000 {
		t.Fatalf("EventBus.MaxMsgs = %d, want 500000", cfg.Storage.EventBus.MaxMsgs)
	}
	if cfg.Storage.EventBus.MaxBytes != 256*1024*1024 {
		t.Fatalf("EventBus.MaxBytes = %d, want 256MiB", cfg.Storage.EventBus.MaxBytes)
	}
	if cfg.Storage.EventBus.MaxInFlight != 128 {
		t.Fatalf("EventBus.MaxInFlight = %d, want 128", cfg.Storage.EventBus.MaxInFlight)
	}
	if cfg.Storage.EventBus.MaxAckPending != 128 {
		t.Fatalf("EventBus.MaxAckPending = %d, want 128", cfg.Storage.EventBus.MaxAckPending)
	}
	if cfg.Storage.EventBus.AckWaitMS != 120000 {
		t.Fatalf("EventBus.AckWaitMS = %d, want 120000", cfg.Storage.EventBus.AckWaitMS)
	}
	if cfg.Storage.EventBus.MaxDeliver != -1 {
		t.Fatalf("EventBus.MaxDeliver = %d, want -1", cfg.Storage.EventBus.MaxDeliver)
	}
	if cfg.Storage.View.AccessScanServiceName != "trpc.moox.storage.AccessScan" {
		t.Fatalf("View.AccessScanServiceName = %q, want AccessScan service", cfg.Storage.View.AccessScanServiceName)
	}
}

func TestStorageEventBusValidateDeliveryLimits(t *testing.T) {
	tests := []struct {
		name    string
		cfg     StorageEventBus
		wantErr string
	}{
		{name: "valid", cfg: StorageEventBus{AckWaitMS: 3000, MaxInFlight: 4, MaxAckPending: 8, MaxDeliver: -1}},
		{name: "ack wait", cfg: StorageEventBus{AckWaitMS: 2999, MaxInFlight: 1, MaxAckPending: 1, MaxDeliver: -1}, wantErr: "ack_wait_ms"},
		{name: "in flight", cfg: StorageEventBus{AckWaitMS: 3000, MaxInFlight: -1, MaxAckPending: 1, MaxDeliver: -1}, wantErr: "max_in_flight"},
		{name: "ack pending", cfg: StorageEventBus{AckWaitMS: 3000, MaxInFlight: 1, MaxAckPending: -1, MaxDeliver: -1}, wantErr: "max_ack_pending"},
		{name: "in flight exceeds pending", cfg: StorageEventBus{AckWaitMS: 3000, MaxInFlight: 9, MaxAckPending: 8, MaxDeliver: -1}, wantErr: "must not exceed"},
		{name: "max deliver", cfg: StorageEventBus{AckWaitMS: 3000, MaxInFlight: 1, MaxAckPending: 1, MaxDeliver: -2}, wantErr: "max_deliver"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
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
	if cfg.Storage.Devices.ParquetPath != "/data/moox/storage/archive" {
		t.Fatalf("Devices.ParquetPath = %q", cfg.Storage.Devices.ParquetPath)
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
			ParquetPath:   "/old/storage/archive",
		},
		EventBus: StorageEventBus{
			Embedded: StorageEmbeddedEventBus{
				StoreDir: "/old/storage/nats",
			},
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
	if cfg.Devices.ParquetPath != "/new/storage/archive" {
		t.Fatalf("Devices.ParquetPath = %q", cfg.Devices.ParquetPath)
	}
	if cfg.EventBus.Embedded.StoreDir != "/new/storage/nats" {
		t.Fatalf("EventBus.Embedded.StoreDir = %q", cfg.EventBus.Embedded.StoreDir)
	}
}

func TestStorageDeploymentConfigFilesLoadRolesAndHealth(t *testing.T) {
	tests := []struct {
		file       string
		wantRole   string
		wantHealth string
	}{
		{file: "storage.access.yaml", wantRole: "access", wantHealth: ":20210"},
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
			if cfg.Storage.Health.Addr != tt.wantHealth {
				t.Fatalf("Health.Addr = %q, want %q", cfg.Storage.Health.Addr, tt.wantHealth)
			}
		})
	}
}

func TestStorageConfigsContainNoLegacyPathsOrRotationAndDependentsDoNotOwnIndexRoot(t *testing.T) {
	files := []string{"storage.yaml", "storage.access.yaml", "storage_view/trpc_go.yaml"}
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

func TestStorageViewMaintenanceDefaults(t *testing.T) {
	var cfg StorageConfig
	cfg.ApplyDefaults()

	maintenance := cfg.View.Maintenance
	if !maintenance.IsEnabled() {
		t.Fatal("maintenance enabled = false, want true")
	}
	if cfg.Devices.ViewIndexRoot != "var/storage/view-indexes" {
		t.Fatalf("view index root = %q, want var/storage/view-indexes", cfg.Devices.ViewIndexRoot)
	}
	if cfg.View.IndexServiceName != "trpc.moox.storage.ViewIndex" {
		t.Fatalf("index service name = %q", cfg.View.IndexServiceName)
	}
	if maintenance.LeaseTTL != "90s" || maintenance.RunBudget != "20s" {
		t.Fatalf("maintenance lease/run = %q/%q", maintenance.LeaseTTL, maintenance.RunBudget)
	}
	if maintenance.MaxEntries != 200000 || maintenance.TargetEntries != 150000 {
		t.Fatalf("entry watermarks = %d/%d", maintenance.MaxEntries, maintenance.TargetEntries)
	}
	if maintenance.MaxPhysicalBytes != 512*1024*1024 || maintenance.MinFreeDiskBytes != 1024*1024*1024 {
		t.Fatalf("byte watermarks = %d/%d", maintenance.MaxPhysicalBytes, maintenance.MinFreeDiskBytes)
	}
	if maintenance.TimeSeries.RetentionByFreq["1d"] != "730d" {
		t.Fatalf("1d retention window = %q, want 730d", maintenance.TimeSeries.RetentionByFreq["1d"])
	}
	if maintenance.Record.RetentionWindow != "30d" {
		t.Fatalf("record retention window = %q, want 30d", maintenance.Record.RetentionWindow)
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

func TestStorageViewMaintenanceYAMLOverrides(t *testing.T) {
	raw := []byte(`
storage:
  devices:
    view_index_root: /indexes
  view:
    index_service_name: custom.ViewIndex
    maintenance:
      enabled: true
      lease_ttl: 2m
      run_budget: 30s
      page_size: 750
      max_entries: 300000
      target_entries: 220000
      max_physical_bytes: 805306368
      min_free_disk_bytes: 2147483648
      min_ready_entries: 8000
      overlap_window: 45m
      allowed_lag: 5m
      remove_grace: 2m
      time_series:
        default_retention_window: 14d
        retention_by_freq:
          1h: 60d
          1d: 1095d
      record:
        retention_window: 45d
`)
	var cfg RuntimeConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()

	maintenance := cfg.Storage.View.Maintenance
	if cfg.Storage.Devices.ViewIndexRoot != "/indexes" || cfg.Storage.View.IndexServiceName != "custom.ViewIndex" {
		t.Fatalf("owner config = %q/%q", cfg.Storage.Devices.ViewIndexRoot, cfg.Storage.View.IndexServiceName)
	}
	if maintenance.MaxEntries != 300000 || maintenance.TargetEntries != 220000 {
		t.Fatalf("entry watermarks = %d/%d", maintenance.MaxEntries, maintenance.TargetEntries)
	}
	if maintenance.TimeSeries.RetentionByFreq["1h"] != "60d" {
		t.Fatalf("1h window = %q, want 60d", maintenance.TimeSeries.RetentionByFreq["1h"])
	}
	if maintenance.Record.RetentionWindow != "45d" {
		t.Fatalf("record retention window = %q, want 45d", maintenance.Record.RetentionWindow)
	}
}

func TestStorageViewMaintenanceYAMLDisableKillSwitch(t *testing.T) {
	raw := []byte(`
storage:
  view:
    maintenance:
      enabled: false
`)
	var cfg RuntimeConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()
	if cfg.Storage.View.Maintenance.IsEnabled() {
		t.Fatal("maintenance enabled = true after explicit false, want false")
	}
}
