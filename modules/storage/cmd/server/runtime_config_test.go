//go:build legacy_storage

package main

import "testing"

func TestLoadRuntimeConfigExtractsStorageFromCombinedTRPCConfig(t *testing.T) {
	cfg, err := loadRuntimeConfig("../../config/storage_view/trpc_go.yaml")
	if err != nil {
		t.Fatalf("loadRuntimeConfig: %v", err)
	}
	if !cfg.Storage.HasRole("view") {
		t.Fatalf("roles = %v, want view", cfg.Storage.Roles)
	}
	if cfg.Storage.Health.Addr != ":20211" {
		t.Fatalf("health addr = %q, want :20211", cfg.Storage.Health.Addr)
	}
}
