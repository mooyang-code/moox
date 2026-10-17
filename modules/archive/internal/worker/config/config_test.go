package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const base = `
gateway_client:
  access_address: access.example.test:11004
  access_id: access@storage
  caller: archive-worker
  key_id: abc123
  key_file: ../secrets/access-archive-worker.key
worker:
  worker_id: archive-worker@testhost
  root_dir: /data/moox-archive
`

func TestLoadAppliesTheDocumentedDefaults(t *testing.T) {
	cfg, err := Load(write(t, base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pacing.RequestSeconds != [2]int{25, 55} || cfg.Pacing.HourQuiet != [2]string{"58:00", "04:00"} || cfg.Pacing.RequestInterval != 200*time.Millisecond ||
		cfg.Pacing.MaxBytesPerSecond != 512<<10 || cfg.Pacing.MaxConcurrentRuns != 4 || cfg.Pacing.MaxRunsPerDataNode != 2 || cfg.Pacing.PageRows != 2000 {
		t.Fatalf("pacing = %+v", cfg.Pacing)
	}
	if cfg.Worker.MinFreeBytes != 5<<30 || cfg.Worker.HeartbeatInterval != 10*time.Second || cfg.Worker.PullInterval != 5*time.Second ||
		cfg.Staging.MemoryLimit != "1GB" || cfg.Staging.Threads != 2 || cfg.Staging.RebuildRatio != 2 || cfg.Health.Addr != "127.0.0.1:11418" || cfg.Maintenance.At != "04:17:43" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.StatePath() != "/data/moox-archive/.state/staging.duckdb" {
		t.Fatalf("state path = %s", cfg.StatePath())
	}
	if !strings.HasSuffix(cfg.StorageSecretPath(), "/secrets/storage-primary-auth.secret") {
		t.Fatalf("storage secret path = %s", cfg.StorageSecretPath())
	}
	if pc := cfg.PacingConfig(); pc.MaxBytesPerSecond != 512<<10 || pc.Validate() != nil {
		t.Fatalf("pacing config = %+v", pc)
	}
}

func TestLoadRejectsBadSettings(t *testing.T) {
	for name, extra := range map[string]string{
		"unknown key":     "mystery: 1\n",
		"relative root":   "",
		"documents root":  "",
		"slow heartbeat":  "worker_override: x\n",
		"bad format":      "",
		"wrong caller":    "",
		"low free":        "",
		"bad quiet":       "",
		"too many pages":  "",
		"bad maintenance": "",
		"per node over":   "",
		"threads":         "",
		"two documents":   "---\nworker:\n  worker_id: x\n",
		"export formats":  "",
	} {
		body := base
		switch name {
		case "relative root":
			body = strings.Replace(base, "/data/moox-archive", "data/archive", 1)
		case "documents root":
			body = strings.Replace(base, "/data/moox-archive", "/Users/me/Documents/archive", 1)
		case "slow heartbeat":
			body = base + "worker:\n  heartbeat_interval: 30s\n"
			body = strings.Replace(body, "worker:\n  worker_id: archive-worker@testhost\n  root_dir: /data/moox-archive\n", "", 1) + "  worker_id: w\n  root_dir: /data/a\n"
		case "bad format":
			body = base + "  export_formats: [parquet]\n"
		case "wrong caller":
			body = strings.Replace(base, "caller: archive-worker", "caller: factor-engine", 1)
		case "low free":
			body = base + "  min_free_bytes: -1\n"
		case "bad quiet":
			body = base + "pacing:\n  hour_quiet: [\"61:00\", \"04:00\"]\n"
		case "too many pages":
			body = base + "pacing:\n  page_rows: 5000\n"
		case "bad maintenance":
			body = base + "maintenance:\n  at: noon\n"
		case "per node over":
			body = base + "pacing:\n  max_concurrent_runs: 2\n  max_runs_per_data_node: 3\n"
		case "threads":
			body = base + "staging:\n  threads: 0\n"
		case "export formats":
			body = base + "  export_formats: []\n"
		default:
			body = base + extra
		}
		if _, err := Load(write(t, body)); err == nil {
			t.Fatalf("%s: Load 应报错", name)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("缺少文件应报错")
	}
}

func TestParseBytesAndClock(t *testing.T) {
	for in, want := range map[string]int64{"5GB": 5 << 30, "512KB": 512 << 10, "1.5MB": 1572864, "100": 100, "2 tb": 2 << 40, "10b": 10} {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Fatalf("ParseBytes(%q) = %d, %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-1GB", "GB"} {
		if _, err := ParseBytes(in); err == nil {
			t.Fatalf("ParseBytes(%q) 应报错", in)
		}
	}
	if d, err := ParseClock("04:17:43"); err != nil || d != 4*time.Hour+17*time.Minute+43*time.Second {
		t.Fatalf("clock = %v %v", d, err)
	}
	if _, err := ParseClock("4pm"); err == nil {
		t.Fatal("非法时刻应报错")
	}
}

func TestShippedWorkerConfigParsesOnceTheCredentialsAreFilledIn(t *testing.T) {
	raw, err := os.ReadFile("../../../config/worker/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), `key_id: ""`, "key_id: abc123", 1)
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GatewayClient.Caller != Caller || cfg.Worker.RootDir != "/data/moox-archive" || cfg.GatewayClient.Compression != "gzip" {
		t.Fatalf("shipped = %+v", cfg)
	}
	if _, err := Load(write(t, string(raw))); err == nil {
		t.Fatal("没有填 key_id 的示例配置不应通过校验")
	}
}
