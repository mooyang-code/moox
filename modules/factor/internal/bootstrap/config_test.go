package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultFactorConfig(t *testing.T) {
	cfg := Default()

	if cfg.Storage.GatewayTarget != "ip://127.0.0.1:11003" {
		t.Fatalf("gateway target = %q", cfg.Storage.GatewayTarget)
	}
	if cfg.NATS.FetchMaxWait != time.Second {
		t.Fatalf("nats fetch max wait = %s", cfg.NATS.FetchMaxWait)
	}
	if len(cfg.NATS.URLs) != 1 || cfg.NATS.URLs[0] != "nats://127.0.0.1:4222" {
		t.Fatalf("nats urls = %v", cfg.NATS.URLs)
	}
	if cfg.Engine.Workers <= 0 {
		t.Fatalf("engine workers = %d, want > 0", cfg.Engine.Workers)
	}
	if cfg.Health.Addr != ":11414" {
		t.Fatalf("health addr = %q", cfg.Health.Addr)
	}
	if cfg.Scheduler.EventBatchWindowMS != 2000 {
		t.Fatalf("event batch window = %d, want 2000", cfg.Scheduler.EventBatchWindowMS)
	}
}

func TestCheckedInConfigUsesEventBatchWindow(t *testing.T) {
	path := filepath.Join("..", "..", "config", "app.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read checked-in config: %v", err)
	}
	legacyKey := "debounce_" + "window_ms"
	if bytes.Contains(data, []byte(legacyKey)) {
		t.Fatalf("checked-in config still contains legacy scheduler key %q", legacyKey)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Scheduler.EventBatchWindowMS != 2000 {
		t.Fatalf("event batch window = %d, want 2000", cfg.Scheduler.EventBatchWindowMS)
	}
	if cfg.NATS.FetchMaxWait != time.Second {
		t.Fatalf("checked-in nats fetch max wait = %s", cfg.NATS.FetchMaxWait)
	}
	if cfg.NATS.CredentialFile != "~/.config/moox/eventbus/factor-eventbus.yaml" {
		t.Fatalf("checked-in credential file = %q", cfg.NATS.CredentialFile)
	}
}

func TestLoadHonorsCustomEventBatchWindow(t *testing.T) {
	cfg, err := Load(writeConfig(t, "scheduler:\n  event_batch_window_ms: 3500\n"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Scheduler.EventBatchWindowMS != 3500 {
		t.Fatalf("event batch window = %d, want 3500", cfg.Scheduler.EventBatchWindowMS)
	}
}

func TestLoadAppliesFactorEnvOverrides(t *testing.T) {
	t.Setenv("MOOX_FACTOR_DB_PATH", "./override/factor.db")
	t.Setenv("MOOX_EVENTBUS_NATS_URL", "tls://eventbus-a.example:4222, tls://eventbus-b.example:4222")
	t.Setenv("MOOX_FACTOR_ENGINE_PYTHON_BIN", "/tmp/factor-python")
	t.Setenv("MOOX_FACTOR_HEALTH_ADDR", "127.0.0.1:16014")

	path := writeConfig(t, `
database:
  path: ./original/factor.db
nats:
  urls: [nats://127.0.0.1:4222]
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.Path != "./override/factor.db" {
		t.Fatalf("database path = %q", cfg.Database.Path)
	}
	if len(cfg.NATS.URLs) != 2 ||
		cfg.NATS.URLs[0] != "tls://eventbus-a.example:4222" ||
		cfg.NATS.URLs[1] != "tls://eventbus-b.example:4222" {
		t.Fatalf("nats urls = %v, want central EventBus override", cfg.NATS.URLs)
	}
	if cfg.Engine.PythonBin != "/tmp/factor-python" {
		t.Fatalf("python bin = %q", cfg.Engine.PythonBin)
	}
	if cfg.Health.Addr != "127.0.0.1:16014" {
		t.Fatalf("health addr = %q", cfg.Health.Addr)
	}
}

func TestLoadRejectsLegacyStorageTargets(t *testing.T) {
	_, err := Load(writeConfig(t, `
storage:
  metadata_target: 127.0.0.1:20100
  access_target: 127.0.0.1:20102
`))
	if err == nil {
		t.Fatal("Load() error = nil, want unknown legacy storage fields")
	}
}

func TestLoadRejectsRemovedNATSURLField(t *testing.T) {
	_, err := Load(writeConfig(t, "nats:\n  url: nats://127.0.0.1:4222\n"))
	if err == nil {
		t.Fatal("Load() error = nil, want removed nats.url rejection")
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
