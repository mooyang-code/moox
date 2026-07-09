package main

import (
	"context"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
)

func TestStorageHealthSnapshot(t *testing.T) {
	cfg := storageconfig.StorageConfig{Roles: []string{"access", "view"}}
	rsp := storageHealthSnapshot(cfg)(context.Background())

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
	cfg := storageconfig.StorageConfig{Roles: []string{"view_query"}}
	rsp := storageHealthSnapshot(cfg)(context.Background())

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
