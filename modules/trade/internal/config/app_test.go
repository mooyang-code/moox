package config

import "testing"

func TestDefaultConfigIncludesSyncDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Sync.Enabled {
		t.Fatalf("sync must be enabled by default")
	}
	if cfg.Sync.WindowHours != 24 {
		t.Fatalf("WindowHours=%d, want 24", cfg.Sync.WindowHours)
	}
	if cfg.Sync.PageSize != 500 {
		t.Fatalf("PageSize=%d, want 500", cfg.Sync.PageSize)
	}
	if cfg.Sync.MaxSymbolsPerRun != 10 {
		t.Fatalf("MaxSymbolsPerRun=%d, want 10", cfg.Sync.MaxSymbolsPerRun)
	}
	if cfg.Health.Addr != ":11210" {
		t.Fatalf("Health.Addr=%q, want :11210", cfg.Health.Addr)
	}
}

func TestLoadAppliesHealthAddrFromEnv(t *testing.T) {
	t.Setenv("MOOX_TRADE_HEALTH_ADDR", "127.0.0.1:16210")

	cfg := DefaultConfig()
	cfg.applyEnv()
	if cfg.Health.Addr != "127.0.0.1:16210" {
		t.Fatalf("Health.Addr=%q, want 127.0.0.1:16210", cfg.Health.Addr)
	}
}
