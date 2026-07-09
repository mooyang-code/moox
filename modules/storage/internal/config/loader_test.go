package config

import (
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

	for _, role := range []string{"access", "view", "view_builder", "view_query", "archive", "primary"} {
		if !cfg.HasRole(role) {
			t.Fatalf("HasRole(%q) = false, want true for all role", role)
		}
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
	if cfg.Storage.EventBus.AckWaitMS != 120000 {
		t.Fatalf("EventBus.AckWaitMS = %d, want 120000", cfg.Storage.EventBus.AckWaitMS)
	}
	if cfg.Storage.EventBus.MaxDeliver != 10 {
		t.Fatalf("EventBus.MaxDeliver = %d, want 10", cfg.Storage.EventBus.MaxDeliver)
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
	if cfg.Storage.Devices.DuckDBPath != "/data/moox/storage/duckdb/views.duckdb" {
		t.Fatalf("Devices.DuckDBPath = %q", cfg.Storage.Devices.DuckDBPath)
	}
	if cfg.Storage.Devices.PebblePath != "/data/moox/storage/pebble" {
		t.Fatalf("Devices.PebblePath = %q", cfg.Storage.Devices.PebblePath)
	}
	if cfg.Storage.Devices.BlevePath != "/data/moox/storage/bleve" {
		t.Fatalf("Devices.BlevePath = %q", cfg.Storage.Devices.BlevePath)
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
			DuckDBPath: "/custom/views.duckdb",
		},
	}

	cfg.ApplyHomeRoot("/data/moox/storage")

	if cfg.Metadata.Path != "/custom/metadata.db" {
		t.Fatalf("Metadata.Path = %q, want /custom/metadata.db", cfg.Metadata.Path)
	}
	if cfg.Devices.DuckDBPath != "/custom/views.duckdb" {
		t.Fatalf("Devices.DuckDBPath = %q, want /custom/views.duckdb", cfg.Devices.DuckDBPath)
	}
}

func TestStorageConfigApplyHomeRootRebasesAbsolutePathsUnderOldRoot(t *testing.T) {
	cfg := StorageConfig{
		Root: "/old/storage",
		Metadata: StorageMetadata{
			Path: "/old/storage/metadata/storage_metadata.db",
		},
		Devices: StorageDevices{
			PebblePath:  "/old/storage/pebble",
			DuckDBPath:  "/old/storage/duckdb/views.duckdb",
			BlevePath:   "/old/storage/bleve",
			ParquetPath: "/old/storage/archive",
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
	if cfg.Devices.DuckDBPath != "/new/storage/duckdb/views.duckdb" {
		t.Fatalf("Devices.DuckDBPath = %q", cfg.Devices.DuckDBPath)
	}
	if cfg.Devices.PebblePath != "/new/storage/pebble" {
		t.Fatalf("Devices.PebblePath = %q", cfg.Devices.PebblePath)
	}
	if cfg.Devices.BlevePath != "/new/storage/bleve" {
		t.Fatalf("Devices.BlevePath = %q", cfg.Devices.BlevePath)
	}
	if cfg.Devices.ParquetPath != "/new/storage/archive" {
		t.Fatalf("Devices.ParquetPath = %q", cfg.Devices.ParquetPath)
	}
	if cfg.EventBus.Embedded.StoreDir != "/new/storage/nats" {
		t.Fatalf("EventBus.Embedded.StoreDir = %q", cfg.EventBus.Embedded.StoreDir)
	}
}

func TestStorageSplitConfigFilesLoadRolesAndHealth(t *testing.T) {
	tests := []struct {
		file       string
		wantRole   string
		wantHealth string
	}{
		{file: "storage.access.yaml", wantRole: "access", wantHealth: ":20210"},
		{file: "storage.view_builder.yaml", wantRole: "view_builder", wantHealth: ":20211"},
		{file: "storage.view_query.yaml", wantRole: "view_query", wantHealth: ":20212"},
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

func TestStorageViewRotationDefaults(t *testing.T) {
	var cfg StorageConfig
	cfg.ApplyDefaults()

	rotation := cfg.View.Rotation
	if !rotation.IsEnabled() {
		t.Fatal("rotation enabled = false, want true")
	}
	if rotation.MaxEntries != 200000 {
		t.Fatalf("max entries = %d, want 200000", rotation.MaxEntries)
	}
	if rotation.MinReadyEntries != 50000 {
		t.Fatalf("min ready entries = %d, want 50000", rotation.MinReadyEntries)
	}
	if rotation.OverlapWindow != "30m" {
		t.Fatalf("overlap window = %q, want 30m", rotation.OverlapWindow)
	}
	if rotation.TimeSeries.FreqBackfillWindow["1d"] != "730d" {
		t.Fatalf("1d backfill window = %q, want 730d", rotation.TimeSeries.FreqBackfillWindow["1d"])
	}
	if rotation.Record.DefaultVersionWindow != "30d" {
		t.Fatalf("record version window = %q, want 30d", rotation.Record.DefaultVersionWindow)
	}
}

func TestStorageViewRotationYAMLOverrides(t *testing.T) {
	raw := []byte(`
storage:
  view:
    rotation:
      enabled: true
      max_entries: 300000
      min_ready_entries: 80000
      overlap_window: 45m
      default_backfill_window: 2d
      allowed_lag: 5m
      remove_grace_ms: 120000
      time_series:
        freq_backfill_window:
          1h: 60d
          1d: 1095d
      record:
        default_version_window: 45d
        max_backfill_entries: 300000
`)
	var cfg RuntimeConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()

	rotation := cfg.Storage.View.Rotation
	if rotation.MaxEntries != 300000 {
		t.Fatalf("max entries = %d, want 300000", rotation.MaxEntries)
	}
	if rotation.TimeSeries.FreqBackfillWindow["1h"] != "60d" {
		t.Fatalf("1h window = %q, want 60d", rotation.TimeSeries.FreqBackfillWindow["1h"])
	}
	if rotation.Record.DefaultVersionWindow != "45d" {
		t.Fatalf("record version window = %q, want 45d", rotation.Record.DefaultVersionWindow)
	}
}

func TestStorageViewRotationYAMLDisableKillSwitch(t *testing.T) {
	raw := []byte(`
storage:
  view:
    rotation:
      enabled: false
`)
	var cfg RuntimeConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()
	if cfg.Storage.View.Rotation.IsEnabled() {
		t.Fatal("rotation enabled = true after explicit false, want false")
	}
}
