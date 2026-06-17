package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	_ "modernc.org/sqlite"
)

func TestConfigPathFromArgs(t *testing.T) {
	if got := configPathFromArgs([]string{"moox-storage", "-conf=./config/trpc_go.yaml"}); got != "./config/trpc_go.yaml" {
		t.Fatalf("configPathFromArgs with equals = %q", got)
	}
	if got := configPathFromArgs([]string{"moox-storage", "-conf", "./config/trpc_go.yaml"}); got != "./config/trpc_go.yaml" {
		t.Fatalf("configPathFromArgs with split flag = %q", got)
	}
}

func TestLoadStorageRootFromConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "trpc_go.yaml")
	if err := os.WriteFile(configPath, []byte("storage:\n  root: ./var/storage\n"), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	root := loadStorageRoot(configPath)
	if root != "./var/storage" {
		t.Fatalf("loadStorageRoot = %q", root)
	}
}

func TestLoadStorageOptionsUsesDeviceAndPrimaryConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "trpc_go.yaml")
	config := []byte(`
storage:
  root: /tmp/moox-storage
  metadata:
    path: /tmp/moox-storage/meta.db
  devices:
    pebble_path: /data/pebble
    duckdb_path: /data/duckdb/views.duckdb
    bleve_path: /data/bleve
  primary:
    service_name: trpc.storage.primary.PrimaryStoreService
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	opts := loadStorageOptions(configPath)
	if opts.PebblePath != "/data/pebble" {
		t.Fatalf("PebblePath = %q", opts.PebblePath)
	}
	if opts.DuckDBPath != "/data/duckdb/views.duckdb" {
		t.Fatalf("DuckDBPath = %q", opts.DuckDBPath)
	}
	if opts.BlevePath != "/data/bleve" {
		t.Fatalf("BlevePath = %q", opts.BlevePath)
	}
	if opts.PrimaryServiceName != "trpc.storage.primary.PrimaryStoreService" {
		t.Fatalf("PrimaryServiceName = %q", opts.PrimaryServiceName)
	}
	if opts.InitSchemaPath != "" {
		t.Fatalf("InitSchemaPath = %q, runtime config must not carry DDL schema path", opts.InitSchemaPath)
	}
}

func TestNewRowsChangedBusSupportsMemoryConfig(t *testing.T) {
	bus, err := newRowsChangedBus(context.Background(), storageconfig.StorageEventBus{Type: "memory"})
	if err != nil {
		t.Fatalf("newRowsChangedBus failed: %v", err)
	}
	if _, ok := bus.(*coreeventbus.MemoryBus); !ok {
		t.Fatalf("bus type = %T", bus)
	}
}

func TestInitMetadataSchemaUsesSchemaNextToConfig(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	schemaDir := filepath.Join(dir, "schema")
	requireMkdirAll(t, configDir)
	requireMkdirAll(t, schemaDir)
	dbPath := filepath.Join(dir, "metadata.db")
	configPath := filepath.Join(configDir, "trpc_go.yaml")
	if err := os.WriteFile(configPath, []byte("storage:\n  metadata:\n    path: "+dbPath+"\n"), 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
	schema := []byte(`
CREATE TABLE IF NOT EXISTS t_spaces (c_id INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS t_datasets (c_id INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS t_storage_routes (c_id INTEGER PRIMARY KEY);
`)
	if err := os.WriteFile(filepath.Join(schemaDir, "storage_metadata.sql"), schema, 0o600); err != nil {
		t.Fatalf("write schema failed: %v", err)
	}

	if err := initMetadataSchema(context.Background(), configPath); err != nil {
		t.Fatalf("initMetadataSchema returned error: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite failed: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='t_spaces'").Scan(&count); err != nil {
		t.Fatalf("query sqlite schema failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("t_spaces table count = %d, want 1", count)
	}
}

func requireMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s failed: %v", path, err)
	}
}
