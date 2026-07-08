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
}
