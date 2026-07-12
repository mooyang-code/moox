package main

import (
	"context"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/health"
	"gopkg.in/yaml.v2"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageTRPCConfigServiceNamesAreUnique(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	if err != nil {
		t.Fatalf("read trpc_go.yaml: %v", err)
	}

	var cfg struct {
		Server struct {
			Service []struct {
				Name string `yaml:"name"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse trpc_go.yaml: %v", err)
	}

	seen := make(map[string]bool)
	for _, service := range cfg.Server.Service {
		if service.Name == "" {
			t.Fatal("server service name must not be empty")
		}
		if seen[service.Name] {
			t.Fatalf("duplicate server service name %q shadows an earlier listener", service.Name)
		}
		seen[service.Name] = true
	}
}

func storageConfigWithRoles(roles ...string) storageconfig.StorageConfig {
	return storageconfig.StorageConfig{Roles: roles}
}

func TestStorageSplitViewRolePredicates(t *testing.T) {
	tests := []struct {
		name        string
		roles       []string
		wantQuery   bool
		wantBuilder bool
		wantIndex   bool
	}{
		{name: "bundled view", roles: []string{"view"}, wantQuery: true, wantBuilder: true, wantIndex: true},
		{name: "query only", roles: []string{"view_query"}, wantQuery: true},
		{name: "builder only", roles: []string{"view_builder"}, wantBuilder: true},
		{name: "index owner only", roles: []string{"view_index"}, wantIndex: true},
		{name: "access only", roles: []string{"access"}},
		{name: "all", roles: []string{"all"}, wantQuery: true, wantBuilder: true, wantIndex: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := storageConfigWithRoles(tt.roles...)
			if got := shouldRegisterViewQueryRole(cfg); got != tt.wantQuery {
				t.Fatalf("shouldRegisterViewQueryRole = %v, want %v", got, tt.wantQuery)
			}
			if got := shouldStartViewBuilderRole(cfg); got != tt.wantBuilder {
				t.Fatalf("shouldStartViewBuilderRole = %v, want %v", got, tt.wantBuilder)
			}
			if got := shouldStartViewIndexRole(cfg); got != tt.wantIndex {
				t.Fatalf("shouldStartViewIndexRole = %v, want %v", got, tt.wantIndex)
			}
		})
	}
}

func TestMaintenanceOwnerIDUsesConfiguredValueOrUniqueStartupSuffix(t *testing.T) {
	if got := maintenanceOwnerID("builder-fixed"); got != "builder-fixed" {
		t.Fatalf("configured owner ID = %q", got)
	}
	left := maintenanceOwnerID("")
	right := maintenanceOwnerID("")
	if left == "" || right == "" || left == right {
		t.Fatalf("generated owner IDs = %q/%q, want unique non-empty IDs", left, right)
	}
}

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
