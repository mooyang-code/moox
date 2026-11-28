package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimal = `
gateway_client:
  caller: archive-mgr
  key_file: ../../secrets/host.key
`

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowedLateness != 15*time.Minute || cfg.Run.LeaseTTL != 10*time.Minute ||
		cfg.StagingRetention != 72*time.Hour || cfg.Database.Path != "./data/archive-mgr/archive.db" {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestLoadRejectsUnknownKeysMultipleDocumentsAndBadValues(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":   minimal + "mystery: 1\n",
		"legacy spaces": minimal + "spaces:\n  stockcn:\n    local_close: \"15:00\"\n",
		"two documents": minimal + "---\ndatabase:\n  path: x\n",
		"wrong caller":  strings.Replace(minimal, "archive-mgr", "someone", 1),
		"short lease":   minimal + "run:\n  lease_ttl: 10s\n",
		"low lateness":  minimal + "allowed_lateness: 5m\n",
		"bad retention": minimal + "retention:\n  other: 24h\n",
		"short staging": minimal + "staging_retention: 1h\n",
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatalf("%s: Load 应报错", name)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("缺少文件应报错")
	}
}

func TestShippedConfigIsValid(t *testing.T) {
	cfg, err := Load("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GatewayClient.Caller != Caller || cfg.AllowedLateness != 15*time.Minute {
		t.Fatalf("shipped config = %+v", cfg)
	}
}

func TestEnvOverridesTheDatabasePath(t *testing.T) {
	t.Setenv("MOOX_ARCHIVE_DB_PATH", "/tmp/override.db")
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil || cfg.Database.Path != "/tmp/override.db" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}
