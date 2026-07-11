package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsAndMarketSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("archive:\n  root_dir: ./archive\n  state_dir: ./state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Archive.DeviceID != "parquet-local" || cfg.Health.Addr != "127.0.0.1:11416" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	want := []string{"crypto_binance", "crypto_okx", "stock_cn", "stock_us"}
	got := cfg.SourceSpaceIDs()
	if len(got) != len(want) {
		t.Fatalf("SourceSpaceIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SourceSpaceIDs() = %v, want %v", got, want)
		}
	}
}

func TestValidateRejectsOverlappingRootAndState(t *testing.T) {
	cfg := Default()
	cfg.Archive.RootDir = "/data/archive"
	cfg.Archive.StateDir = "/data/archive/state"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must not overlap") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsCOSWithoutLocation(t *testing.T) {
	cfg := Default()
	cfg.Archive.COS.Enabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "region and bucket") {
		t.Fatalf("Validate() error = %v", err)
	}
}
