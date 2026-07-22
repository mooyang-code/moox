package config

import (
	"github.com/stretchr/testify/assert"
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
	if cfg.Archive.EventBus.Stream != "MOOX_STORAGE" || cfg.Archive.EventBus.Durable != "moox_archive_kline_v1" {
		t.Fatalf("eventbus topology reference = %#v", cfg.Archive.EventBus)
	}
	if cfg.Archive.EventBus.CredentialFile != "" {
		t.Fatalf("default eventbus credential file = %q, want empty for development", cfg.Archive.EventBus.CredentialFile)
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

func TestPathContains(t *testing.T) {
	parent := filepath.Clean("/data/archive")
	child := filepath.Clean("/data/archive/state")
	assert.True(t, pathContains(parent, child))
	assert.False(t, pathContains("/data/other", child))
}

func TestAppConfigHasNoProcessOwnedScheduleIntervals(t *testing.T) {
	raw, err := os.ReadFile("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, oldKey := range []string{"interval:", "sync_interval:"} {
		if strings.Contains(string(raw), oldKey) {
			t.Fatalf("app config still contains %q", oldKey)
		}
	}
	for _, required := range []string{"stream: MOOX_STORAGE", "durable: moox_archive_kline_v1", "fetch_max_wait: 1s"} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("app config does not contain %q", required)
		}
	}
	if !strings.Contains(string(raw), "credential_file: ~/.config/moox/eventbus/archive-eventbus.yaml") {
		t.Fatal("app config does not name the archive EventBus credential file")
	}
}
