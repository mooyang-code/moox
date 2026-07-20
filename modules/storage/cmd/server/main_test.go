//go:build legacy_storage

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
		{name: "primary only", roles: []string{"primary"}},
		{name: "shard only", roles: []string{"shard"}},
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
	cfg := storageconfig.StorageConfig{Root: t.TempDir(), Metadata: storageconfig.StorageMetadata{Path: filepath.Join(t.TempDir(), "metadata.db")}, Roles: []string{"primary", "view"}}
	if err := os.WriteFile(cfg.Metadata.Path, nil, 0o644); err != nil {
		t.Fatalf("create metadata file: %v", err)
	}
	state := health.New("storage", "storage", "", "")
	rsp := storageHealthSnapshot(cfg, state, storageHealthDependencies{eventbus: fakeReadyState(true), view: fakeReadyState(true), primary: fakeReadyState(true)})(context.Background())

	if rsp.Module != "storage" || !rsp.Ready || rsp.Status != "ok" {
		t.Fatalf("health retinfo = %+v", rsp)
	}
	if rsp.Details["roles"] != "primary,view" {
		t.Fatalf("roles = %v", rsp.Details["roles"])
	}
	if rsp.Service != "storage-primary-view" || rsp.Details["service"] != "storage-primary-view" || rsp.Details["role"] != "primary,view" {
		t.Fatalf("service metadata = service:%q details:%+v", rsp.Service, rsp.Details)
	}
}

func TestStorageHealthSnapshotReportsMissingDependencies(t *testing.T) {
	cfg := storageconfig.StorageConfig{Roles: []string{"primary"}}
	state := health.New("storage", "storage", "", "")
	rsp := storageHealthSnapshot(cfg, state, storageHealthDependencies{})(context.Background())
	if rsp.Ready {
		t.Fatalf("health retinfo = %+v, want not ready", rsp)
	}
}

func TestStorageHealthSnapshotRequiresUnifiedViewRuntimeAndEventBus(t *testing.T) {
	cfg := storageconfig.StorageConfig{
		Root: t.TempDir(), Metadata: storageconfig.StorageMetadata{Path: filepath.Join(t.TempDir(), "metadata.db")}, Roles: []string{"view"},
	}
	if err := os.WriteFile(cfg.Metadata.Path, nil, 0o644); err != nil {
		t.Fatalf("create metadata file: %v", err)
	}
	state := health.New("storage", "storage-view", "", "")
	rsp := storageHealthSnapshot(cfg, state, storageHealthDependencies{eventbus: fakeReadyState(false)})(context.Background())
	if rsp.Service != "storage-view" || rsp.InstanceID != "storage-view" {
		t.Fatalf("health identity = %q/%q, want storage-view/storage-view", rsp.Service, rsp.InstanceID)
	}
	if rsp.Ready {
		t.Fatalf("health retinfo = %+v, want not ready", rsp)
	}
	if rsp.Details["eventbus_ready"] != false || rsp.Details["view_runtime_ready"] != false {
		t.Fatalf("health details = %+v", rsp.Details)
	}
}

type fakeReadyState bool

func (s fakeReadyState) Ready() bool { return bool(s) }
