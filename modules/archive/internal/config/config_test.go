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
  key_id: abc
  key_file: ../../secrets/caller-archive-mgr.key
`

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Schedule.MinInterval != 5*time.Minute || cfg.Schedule.MinLookback != 15*time.Minute || cfg.Run.LeaseTTL != 10*time.Minute ||
		cfg.Worker.TakeoverWait != 45*time.Second || cfg.StagingRetention != 72*time.Hour || cfg.Database.Path != "./data/archive-mgr/archive.db" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cn := cfg.Space("StockCN"); cn.TZ != "Asia/Shanghai" || cn.LocalClose != "15:00" {
		t.Fatalf("stockcn = %+v", cn)
	}
	if unknown := cfg.Space("newspace"); unknown.TZ != "UTC" || unknown.LocalClose != "" {
		t.Fatalf("未配置的空间按 UTC、全天交易: %+v", unknown)
	}
}

func TestLoadRejectsUnknownKeysMultipleDocumentsAndBadValues(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":    minimal + "mystery: 1\n",
		"two documents":  minimal + "---\ndatabase:\n  path: x\n",
		"wrong caller":   strings.Replace(minimal, "archive-mgr", "someone", 1),
		"short lease":    minimal + "run:\n  lease_ttl: 10s\n",
		"low interval":   minimal + "schedule:\n  min_interval: 1m\n",
		"low lookback":   minimal + "schedule:\n  min_lookback: 5m\n",
		"bad takeover":   minimal + "worker:\n  takeover_wait: 1s\n",
		"bad retention":  minimal + "retention:\n  scheduled_success: 720h\n  other: 24h\n",
		"short staging":  minimal + "staging_retention: 1h\n",
		"bad tz":         minimal + "spaces:\n  crypto:\n    tz: Mars/Base\n",
		"bad close":      minimal + "spaces:\n  stockcn:\n    tz: Asia/Shanghai\n    local_close: \"25:61\"\n",
		"upper space id": minimal + "spaces:\n  Crypto:\n    tz: UTC\n",
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
	if cfg.GatewayClient.Caller != Caller || cfg.Space("stockus").TZ != "America/New_York" {
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
