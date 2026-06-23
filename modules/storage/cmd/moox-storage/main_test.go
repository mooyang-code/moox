package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	_ "modernc.org/sqlite"
)

func TestMainDoesNotImportConcreteStorageImplementations(t *testing.T) {
	content, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go failed: %v", err)
	}
	text := string(content)
	for _, forbidden := range []string{
		"internal/infra/metadata/sqlite",
		"internal/infra/transport/nats",
		"internal/infra/transport\"",
		"internal/infra/eventbus",
		"internal/core/eventbus",
		"internal/runtime/",
		"eventbusbootstrap",
		"metadatabootstrap",
		"context.Background()",
		"trpc.Background()",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("main.go should not import concrete storage implementation %q", forbidden)
		}
	}
	for _, want := range []string{
		"internal/bootstrap/eventbus",
		"internal/bootstrap/metadata",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("main.go should import bootstrap package %q", want)
		}
	}
	if !strings.Contains(text, "trpc.BackgroundContext()") {
		t.Fatalf("main.go should create root contexts with trpc.BackgroundContext()")
	}
}

func TestConfigPathFromArgs(t *testing.T) {
	if got := configPathFromArgs([]string{"moox-storage", "-conf=./config/trpc_go.yaml"}); got != "./config/trpc_go.yaml" {
		t.Fatalf("configPathFromArgs with equals = %q", got)
	}
	if got := configPathFromArgs([]string{"moox-storage", "-conf", "./config/trpc_go.yaml"}); got != "./config/trpc_go.yaml" {
		t.Fatalf("configPathFromArgs with split flag = %q", got)
	}
}

func TestStorageConfigPathFromArgs(t *testing.T) {
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	frameworkPath := filepath.Join("config", "trpc_go.yaml")
	wantDefault := filepath.Join("config", "storage.yaml")
	if got := storageConfigPathFromArgs([]string{"moox-storage"}, frameworkPath); got != wantDefault {
		t.Fatalf("storageConfigPathFromArgs default = %q, want %q", got, wantDefault)
	}
	if got := storageConfigPathFromArgs([]string{"moox-storage", "-storage-conf=./config/storage.yaml"}, frameworkPath); got != "./config/storage.yaml" {
		t.Fatalf("storageConfigPathFromArgs with equals = %q", got)
	}
	if got := storageConfigPathFromArgs([]string{"moox-storage", "--storage-conf", "./config/storage.yaml"}, frameworkPath); got != "./config/storage.yaml" {
		t.Fatalf("storageConfigPathFromArgs with split flag = %q", got)
	}
	t.Setenv("MOOX_STORAGE_CONFIG", "/tmp/moox/storage.yaml")
	if got := storageConfigPathFromArgs([]string{"moox-storage"}, frameworkPath); got != "/tmp/moox/storage.yaml" {
		t.Fatalf("storageConfigPathFromArgs env = %q", got)
	}
}

func TestStorageRolesDefaultToAccessAndDeriver(t *testing.T) {
	var cfg storageconfig.RuntimeConfig
	cfg.ApplyDefaults()
	if !cfg.Storage.HasRole("access") {
		t.Fatalf("default storage roles should include access")
	}
	if !cfg.Storage.HasRole("deriver") {
		t.Fatalf("default storage roles should include deriver")
	}
	if cfg.Storage.HasRole("primary") {
		t.Fatalf("default storage roles should not include primary")
	}
}

func TestRoleEnabledIsCaseInsensitive(t *testing.T) {
	cfg := storageconfig.StorageConfig{Roles: []string{" Access ", "DERIVER"}}
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

func TestLoadStorageOptionsUsesDeviceAndPrimaryConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "storage.yaml")
	config := []byte(`
storage:
  root: /tmp/moox-storage
  metadata:
    path: /tmp/moox-storage/meta.db
  devices:
    pebble_path: /data/pebble
    duckdb_path: /data/duckdb/views.duckdb
    bleve_path: /data/bleve
    parquet_path: /data/archive
  primary:
    service_name: trpc.storage.store.PrimaryStoreService
  eventbus:
    type: nats
    nats_url: nats://127.0.0.1:4222
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
	if opts.ParquetPath != "/data/archive" {
		t.Fatalf("ParquetPath = %q", opts.ParquetPath)
	}
	if opts.PrimaryServiceName != "trpc.storage.store.PrimaryStoreService" {
		t.Fatalf("PrimaryServiceName = %q", opts.PrimaryServiceName)
	}
	if opts.InitSchemaPath != "" {
		t.Fatalf("InitSchemaPath = %q, runtime config must not carry DDL schema path", opts.InitSchemaPath)
	}
	if opts.Events != nil {
		t.Fatalf("Events = %#v, loadStorageOptions must not initialize eventbus", opts.Events)
	}
}

func TestLoadStorageOptionsDefaultsToLocalPrimary(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "storage.yaml")
	config := []byte(`
storage:
  root: /tmp/moox-storage
  devices:
    pebble_path: /data/pebble
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	opts := loadStorageOptions(configPath)
	if opts.PrimaryServiceName != "" {
		t.Fatalf("PrimaryServiceName = %q, want empty for local primary", opts.PrimaryServiceName)
	}
}

func TestRoleHelpers(t *testing.T) {
	primaryOnly := storageconfig.StorageConfig{Roles: []string{"primary"}}
	if needsRowsChangedBus(primaryOnly) {
		t.Fatalf("primary-only role should not need row changed eventbus")
	}
	if shouldCreateStorageService(primaryOnly) {
		t.Fatalf("primary-only role should not create access storage service")
	}
	if !shouldCreatePrimaryService(primaryOnly) {
		t.Fatalf("primary-only role should create primary service")
	}

	accessDeriver := storageconfig.StorageConfig{Roles: []string{"access", "deriver"}}
	if !needsRowsChangedBus(accessDeriver) {
		t.Fatalf("access+deriver roles should need row changed eventbus")
	}
	if !shouldCreateStorageService(accessDeriver) {
		t.Fatalf("access+deriver roles should create access storage service")
	}
	if shouldCreatePrimaryService(accessDeriver) {
		t.Fatalf("access+deriver roles should not create primary service")
	}
}

func TestDeriverUsesLocalAccessReaderOnlyForInProcessMemoryAccess(t *testing.T) {
	memoryAccess := storageconfig.StorageConfig{
		Roles:    []string{"access", "deriver"},
		EventBus: storageconfig.StorageEventBus{Type: "memory"},
	}
	if !shouldUseLocalAccessReader(memoryAccess) {
		t.Fatalf("access+deriver with memory eventbus should use local access reader")
	}

	natsAccess := storageconfig.StorageConfig{
		Roles:    []string{"access", "deriver"},
		EventBus: storageconfig.StorageEventBus{Type: "nats"},
	}
	if shouldUseLocalAccessReader(natsAccess) {
		t.Fatalf("access+deriver with nats eventbus should use remote access reader")
	}

	memoryDeriverOnly := storageconfig.StorageConfig{
		Roles:    []string{"deriver"},
		EventBus: storageconfig.StorageEventBus{Type: "memory"},
	}
	if shouldUseLocalAccessReader(memoryDeriverOnly) {
		t.Fatalf("deriver-only with memory eventbus should not use local access reader")
	}
}

func TestRepositoryConfigUsesUnifiedEventSubjectPrefix(t *testing.T) {
	cfg, ok := loadStorageConfig(filepath.Join("..", "..", "config", "storage.yaml"))
	if !ok {
		t.Fatalf("load repository config failed")
	}
	if cfg.Storage.EventBus.SubjectPrefix != "moox.storage" {
		t.Fatalf("subject_prefix = %q", cfg.Storage.EventBus.SubjectPrefix)
	}
}

func TestRepositoryUsesViewProtocolFileNames(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, path := range []string{
		filepath.Join(root, "proto", "view.proto"),
		filepath.Join(root, "proto", "gen", "view.pb.go"),
		filepath.Join(root, "proto", "gen", "view.trpc.go"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected view protocol artifact %s: %v", path, err)
		}
	}
	legacyPrefix := "deri" + "ved"
	for _, path := range []string{
		filepath.Join(root, "proto", legacyPrefix+".proto"),
		filepath.Join(root, "proto", "gen", legacyPrefix+".pb.go"),
		filepath.Join(root, "proto", "gen", legacyPrefix+".trpc.go"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy view protocol artifact %s should not exist", path)
		}
	}
	makefile, err := os.ReadFile(filepath.Join(root, "proto", "Makefile"))
	if err != nil {
		t.Fatalf("read proto Makefile failed: %v", err)
	}
	text := string(makefile)
	if !strings.Contains(text, "view.proto") || strings.Contains(text, legacyPrefix+".proto") {
		t.Fatalf("proto Makefile must generate view.proto and not legacy protocol file")
	}
}

func TestAccessServiceUsesViewErrorReporterNames(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, path := range []string{
		filepath.Join(root, "internal", "services", "access", "options.go"),
		filepath.Join(root, "internal", "services", "access", "service.go"),
		filepath.Join(root, "internal", "services", "access", "data.go"),
		filepath.Join(root, "internal", "services", "access", "query.go"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s failed: %v", path, err)
		}
		text := string(content)
		legacyPrefix := "Deri" + "ved"
		for _, forbidden := range []string{
			legacyPrefix + "ErrorReporter",
			legacyPrefix + "Errors",
			"report" + legacyPrefix + "Error",
			"log" + legacyPrefix + "Error",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s should not contain legacy view error reporter name %q", path, forbidden)
			}
		}
	}
}

func TestRepositoryFrameworkConfigDoesNotContainStorageBusinessConfig(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	if err != nil {
		t.Fatalf("read repository config failed: %v", err)
	}
	text := string(content)
	if strings.Contains(text, "\nstorage:") || strings.HasPrefix(text, "storage:") {
		t.Fatalf("trpc_go.yaml must not contain storage business config")
	}
}

func TestRepositoryConfigDefinesStorageTimers(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	if err != nil {
		t.Fatalf("read repository config failed: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"name: trpc.storage.view.timer",
		"name: trpc.storage.view.cleanup.timer",
		"name: trpc.storage.view.retry_failed.timer",
		"name: trpc.storage.archive.timer",
		"protocol: timer",
		"scheduler=viewBuilderSchedule&params=op=cleanup",
		"scheduler=viewBuilderSchedule&params=op=retry_failed",
		"scheduler=archiveSchedule",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("repository config missing %q", want)
		}
	}
}

func TestRegisterTimerHandlerServiceSkipsMissingService(t *testing.T) {
	registered := registerTimerHandlerService("trpc.storage.missing.timer", nil, func(context.Context, string) error {
		return nil
	})
	if registered {
		t.Fatalf("registerTimerHandlerService returned true for missing service")
	}
}

func TestInitMetadataSchemaUsesSchemaNextToConfig(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	schemaDir := filepath.Join(dir, "schema")
	requireMkdirAll(t, configDir)
	requireMkdirAll(t, schemaDir)
	dbPath := filepath.Join(dir, "metadata.db")
	trpcConfigPath := filepath.Join(configDir, "trpc_go.yaml")
	storageConfigPath := filepath.Join(configDir, "storage.yaml")
	if err := os.WriteFile(trpcConfigPath, []byte("global:\n  namespace: Development\n"), 0o600); err != nil {
		t.Fatalf("write trpc config failed: %v", err)
	}
	if err := os.WriteFile(storageConfigPath, []byte("storage:\n  metadata:\n    path: "+dbPath+"\n"), 0o600); err != nil {
		t.Fatalf("write storage config failed: %v", err)
	}
	schema := []byte(`
CREATE TABLE IF NOT EXISTS t_spaces (c_id INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS t_datasets (c_id INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS t_primary_store_routes (c_id INTEGER PRIMARY KEY);
`)
	if err := os.WriteFile(filepath.Join(schemaDir, "metadata.sql"), schema, 0o600); err != nil {
		t.Fatalf("write schema failed: %v", err)
	}

	if err := initMetadataSchema(context.Background(), trpcConfigPath, storageConfigPath); err != nil {
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
