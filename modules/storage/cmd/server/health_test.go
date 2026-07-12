package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/health"
)

func TestStorageHealthSnapshot(t *testing.T) {
	cfg := storageconfig.StorageConfig{Root: t.TempDir(), Metadata: storageconfig.StorageMetadata{Path: filepath.Join(t.TempDir(), "metadata.db")}, Roles: []string{"access", "view"}}
	if err := os.WriteFile(cfg.Metadata.Path, nil, 0o644); err != nil {
		t.Fatalf("create metadata file: %v", err)
	}
	state := health.New("storage", "storage", "", "")
	rsp := storageHealthSnapshot(cfg, state)(context.Background())

	if rsp.Module != "storage" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["roles"] != "access,view" {
		t.Fatalf("roles = %v", rsp.Details["roles"])
	}
	if rsp.Service != "storage" || rsp.Details["service"] != "storage" || rsp.Details["role"] != "access,view" {
		t.Fatalf("service metadata = service:%q details:%+v", rsp.Service, rsp.Details)
	}
}

func TestStorageHealthSnapshotNamesSplitViewQueryRole(t *testing.T) {
	cfg := storageconfig.StorageConfig{Root: t.TempDir(), Metadata: storageconfig.StorageMetadata{Path: filepath.Join(t.TempDir(), "metadata.db")}, Roles: []string{"view_query"}}
	if err := os.WriteFile(cfg.Metadata.Path, nil, 0o644); err != nil {
		t.Fatalf("create metadata file: %v", err)
	}
	state := health.New("storage", "storage", "", "")
	rsp := storageHealthSnapshot(cfg, state)(context.Background())

	if rsp.Service != "storage-view-query" {
		t.Fatalf("Service = %q, want storage-view-query", rsp.Service)
	}
	if rsp.InstanceID != "storage-view-query" {
		t.Fatalf("InstanceID = %q, want storage-view-query", rsp.InstanceID)
	}
	if rsp.Details["role"] != "view_query" {
		t.Fatalf("role = %v, want view_query", rsp.Details["role"])
	}
}

func TestStorageHealthSnapshotNamesViewIndexOwnerRole(t *testing.T) {
	cfg := storageconfig.StorageConfig{Root: t.TempDir(), Metadata: storageconfig.StorageMetadata{Path: filepath.Join(t.TempDir(), "metadata.db")}, Roles: []string{"view_index"}}
	if err := os.WriteFile(cfg.Metadata.Path, nil, 0o644); err != nil {
		t.Fatalf("create metadata file: %v", err)
	}
	state := health.New("storage", "storage", "", "")
	rsp := storageHealthSnapshot(cfg, state)(context.Background())

	if rsp.Service != "storage-view-index" {
		t.Fatalf("Service = %q, want storage-view-index", rsp.Service)
	}
}

func TestStorageHealthSnapshotReportsMissingDependencies(t *testing.T) {
	cfg := storageconfig.StorageConfig{Roles: []string{"access"}}
	state := health.New("storage", "storage", "", "")
	rsp := storageHealthSnapshot(cfg, state)(context.Background())
	if rsp.Ready {
		t.Fatalf("health response = %+v, want not ready", rsp)
	}
}
