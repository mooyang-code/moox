package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInitCreatesMetadataTables(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "storage_metadata.db")
	confPath := writeStorageConfig(t, tmp, dbPath)

	var stdout, stderr bytes.Buffer
	if err := runCommand([]string{"init", "--storage-conf", confPath, "--schema-path", schemaPath(t)}, &stdout, &stderr); err != nil {
		t.Fatalf("run init: %v stderr=%s", err, stderr.String())
	}

	var out struct {
		Module string `json:"module"`
		Action string `json:"action"`
		Status string `json:"status"`
		DBPath string `json:"db_path"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Module != "storage" || out.Action != "init" || out.Status != "ok" {
		t.Fatalf("unexpected init output: %+v", out)
	}
	if out.DBPath != dbPath {
		t.Fatalf("db_path = %q, want %q", out.DBPath, dbPath)
	}

	assertTableExists(t, dbPath, "t_spaces")
	assertTableExists(t, dbPath, "t_datasets")
	assertTableExists(t, dbPath, "t_primary_store_routes")
}

func TestImportSeedInitializesSchemaAndImportsSeed(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "storage_metadata.db")
	confPath := writeStorageConfig(t, tmp, dbPath)

	var stdout, stderr bytes.Buffer
	err := runCommand([]string{
		"import-seed",
		"--storage-conf", confPath,
		"--schema-path", schemaPath(t),
		"--seed", seedPath(t),
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run import-seed: %v stderr=%s", err, stderr.String())
	}

	var out struct {
		Module  string `json:"module"`
		Action  string `json:"action"`
		Status  string `json:"status"`
		DBPath  string `json:"db_path"`
		Seed    string `json:"seed_path"`
		Summary struct {
			Spaces            int `json:"spaces"`
			Datasets          int `json:"datasets"`
			PrimaryStoreRoute int `json:"primary_store_routes"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	if out.Module != "storage" || out.Action != "import-seed" || out.Status != "ok" {
		t.Fatalf("unexpected import output: %+v", out)
	}
	if out.DBPath != dbPath {
		t.Fatalf("db_path = %q, want %q", out.DBPath, dbPath)
	}
	if out.Seed != seedPath(t) {
		t.Fatalf("seed_path = %q, want %q", out.Seed, seedPath(t))
	}
	if out.Summary.Spaces == 0 || out.Summary.Datasets == 0 || out.Summary.PrimaryStoreRoute == 0 {
		t.Fatalf("summary did not report imported seed entities: %+v", out.Summary)
	}
	assertRowCountAtLeast(t, dbPath, "t_spaces", 1)
	assertRowCountAtLeast(t, dbPath, "t_datasets", 1)
}

func writeStorageConfig(t *testing.T, root string, dbPath string) string {
	t.Helper()
	confPath := filepath.Join(root, "storage.yaml")
	raw := []byte("storage:\n  root: " + root + "\n  metadata:\n    path: " + dbPath + "\n")
	if err := os.WriteFile(confPath, raw, 0o644); err != nil {
		t.Fatalf("write storage config: %v", err)
	}
	return confPath
}

func schemaPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "schema", "metadata.sql"))
	if err != nil {
		t.Fatalf("resolve schema path: %v", err)
	}
	return path
}

func seedPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "config", "metadata.seed.yaml"))
	if err != nil {
		t.Fatalf("resolve seed path: %v", err)
	}
	return path
}

func assertTableExists(t *testing.T, dbPath string, table string) {
	t.Helper()
	db := openTestDB(t, dbPath)
	defer db.Close()
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name)
	if err != nil {
		t.Fatalf("table %s not found: %v", table, err)
	}
}

func assertRowCountAtLeast(t *testing.T, dbPath string, table string, min int) {
	t.Helper()
	db := openTestDB(t, dbPath)
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count < min {
		t.Fatalf("%s rows = %d, want at least %d", table, count, min)
	}
}

func openTestDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}
