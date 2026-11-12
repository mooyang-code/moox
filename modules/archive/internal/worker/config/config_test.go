package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testRoot = "/data/moox-archive"

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var base = `
gateway_client:
  access_address: access.example.test:11004
  access_id: access@storage
  caller: archive-worker
  key_id: abc123
  key_file: ../secrets/access-archive-worker.key
worker:
  worker_id: archive-worker@testhost
  root_dir: ` + testRoot + `
`

func TestLoadAppliesTheDocumentedDefaults(t *testing.T) {
	cfg, err := Load(write(t, base))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pacing.RequestSeconds != [2]int{25, 55} || cfg.Pacing.HourQuiet != [2]string{"58:00", "04:00"} || cfg.Pacing.RequestInterval != 200*time.Millisecond ||
		cfg.Pacing.MaxBytesPerSecond != 512<<10 || cfg.Pacing.MaxConcurrentRuns != 4 || cfg.Pacing.MaxStorageRuns != 2 || cfg.Pacing.PageRows != 500 {
		t.Fatalf("pacing = %+v", cfg.Pacing)
	}
	if cfg.Worker.MinFreeBytes != 5<<30 || cfg.Worker.HeartbeatInterval != 10*time.Second || cfg.Worker.PullInterval != 5*time.Second ||
		cfg.Staging.CacheSize != 256<<20 || cfg.Worker.ExportFormat != "csv.gz" || cfg.Health.Addr != "127.0.0.1:11418" || cfg.Maintenance.At != "04:17:43" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if DataAddr() != "127.0.0.1:11428" || cfg.DataAPI.MaxBytesPerSecond != 2<<20 {
		t.Fatalf("data_api = %+v", cfg.DataAPI)
	}
	if cfg.StatePath() != filepath.Join(testRoot, ".state", "staging.db") {
		t.Fatalf("state path = %s", cfg.StatePath())
	}
	if !strings.HasSuffix(filepath.ToSlash(cfg.StorageSecretPath()), "/secrets/storage-primary-auth.secret") {
		t.Fatalf("storage secret path = %s", cfg.StorageSecretPath())
	}
	if pc := cfg.PacingConfig(); pc.MaxBytesPerSecond != 512<<10 || pc.Validate() != nil {
		t.Fatalf("pacing config = %+v", pc)
	}
}

func TestLoadRejectsBadSettings(t *testing.T) {
	for name, extra := range map[string]string{
		"unknown key":       "mystery: 1\n",
		"relative root":     "",
		"documents root":    "",
		"slow heartbeat":    "worker_override: x\n",
		"bad format":        "",
		"wrong caller":      "",
		"low free":          "",
		"bad quiet":         "",
		"too many pages":    "",
		"bad maintenance":   "",
		"storage runs over": "",
		"small cache":       "",
		"two documents":     "---\nworker:\n  worker_id: x\n",
		"export formats":    "",
		"removed listen":    "data_api:\n  listen: 127.0.0.1:11428\n",
		"slow data api":     "data_api:\n  max_bytes_per_second: 255KB\n",
	} {
		body := base
		switch name {
		case "relative root":
			body = strings.Replace(base, testRoot, "data/archive", 1)
		case "documents root":
			body = strings.Replace(base, testRoot, "/Users/me/Documents/archive", 1)
		case "slow heartbeat":
			body = base + "worker:\n  heartbeat_interval: 30s\n"
			body = strings.Replace(body, "worker:\n  worker_id: archive-worker@testhost\n  root_dir: "+testRoot+"\n", "", 1) + "  worker_id: w\n  root_dir: /data/a\n"
		case "bad format":
			body = base + "  export_format: parquet\n"
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
		case "storage runs over":
			body = base + "pacing:\n  max_concurrent_runs: 2\n  max_storage_runs: 3\n"
		case "small cache":
			body = base + "staging:\n  cache_size: 64KB\n"
		case "export formats":
			body = base + "  export_formats: [csv]\n"
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
	if cfg.GatewayClient.Caller != Caller || cfg.Worker.RootDir != testRoot {
		t.Fatalf("shipped = %+v", cfg)
	}
	if _, err := Load(write(t, string(raw))); err == nil {
		t.Fatal("没有填 key_id 的示例配置不应通过校验")
	}
}

func TestExportFormatAcceptsPlainCSV(t *testing.T) {
	cfg, err := Load(write(t, strings.Replace(base, "worker:\n", "worker:\n  export_format: csv\n", 1)))
	if err != nil || cfg.Worker.ExportFormat != "csv" {
		t.Fatalf("cfg=%v err=%v", cfg, err)
	}
}

func TestMinuteWindowDefaultsOnAndCanBeSwitchedOff(t *testing.T) {
	cfg, err := Load(write(t, base))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Pacing.MinuteWindow || !cfg.PacingConfig().MinuteWindow {
		t.Fatalf("minute_window 默认应开启: %+v", cfg.Pacing)
	}
	off, err := Load(write(t, base+"pacing:\n  minute_window: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if off.Pacing.MinuteWindow || off.PacingConfig().MinuteWindow {
		t.Fatalf("minute_window: false 应关闭: %+v", off.Pacing)
	}
}

const objectStoreSection = `object_store:
  endpoint: cos.ap-nanjing.myqcloud.com
  region: ap-nanjing
  bucket: moox-archive-1250000000
  url_style: virtual
  storage_class: STANDARD_IA
  credentials_file: ../secrets/archive-object-store.env
`

func TestObjectStoreSectionIsOptionalAndValidated(t *testing.T) {
	cfg, err := Load(write(t, base))
	if err != nil || cfg.ObjectStore != nil || cfg.ObjectStoreCredentialsPath() != "" {
		t.Fatalf("省略整段表示不使用: %+v %v", cfg.ObjectStore, err)
	}
	if client, err := cfg.OpenObjectStore(); client != nil || err != nil {
		t.Fatalf("没配时不创建客户端: %v %v", client, err)
	}
	path := write(t, base+objectStoreSection)
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ObjectStore.Prefix != "moox-archive/" || !cfg.ObjectStore.TLS() || cfg.ObjectStore.StorageClass != "STANDARD_IA" {
		t.Fatalf("object_store = %+v", cfg.ObjectStore)
	}
	if want := filepath.Join(filepath.Dir(filepath.Dir(path)), "secrets", "archive-object-store.env"); cfg.ObjectStoreCredentialsPath() != want {
		t.Fatalf("凭据路径 = %s, want %s", cfg.ObjectStoreCredentialsPath(), want)
	}
	if _, err := cfg.OpenObjectStore(); err == nil {
		t.Fatal("凭据文件不存在时应报错")
	}
	for name, section := range map[string]string{
		"未知字段":         objectStoreSection + "  part_size: 64MiB\n",
		"url_style 缺失": strings.Replace(objectStoreSection, "  url_style: virtual\n", "", 1),
		"region 缺失":    strings.Replace(objectStoreSection, "  region: ap-nanjing\n", "", 1),
	} {
		if _, err := Load(write(t, base+section)); err == nil {
			t.Fatalf("%s：应被拒", name)
		}
	}
}

func TestLoadObjectStoreReadsOnlyThatSection(t *testing.T) {
	// 只有 object_store 段（本机冒烟的临时配置）：完整的 worker 校验会因缺 gateway_client 失败，这里不受影响。
	cfg, err := LoadObjectStore(write(t, "pacing: {whatever: 1}\n"+objectStoreSection))
	if err != nil || cfg.ObjectStore.Bucket != "moox-archive-1250000000" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	if _, err := LoadObjectStore(write(t, base)); err == nil || !strings.Contains(err.Error(), "没有 object_store 段") {
		t.Fatalf("缺段应报错: %v", err)
	}
	if _, err := LoadObjectStore(write(t, objectStoreSection+"  mystery: 1\n")); err == nil {
		t.Fatal("段内未知字段应被拒")
	}
}
