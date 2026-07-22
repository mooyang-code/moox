package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultFactorConfig(t *testing.T) {
	cfg := Default()

	if cfg.Storage.GatewayTarget != "ip://127.0.0.1:11003" {
		t.Fatalf("gateway target = %q", cfg.Storage.GatewayTarget)
	}
	if cfg.NATS.Stream != "MOOX_STORAGE" {
		t.Fatalf("nats stream = %q", cfg.NATS.Stream)
	}
	if cfg.NATS.Subject != "moox.storage.fields_changed.v1.>" {
		t.Fatalf("nats subject = %q", cfg.NATS.Subject)
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
	if cfg.NATS.Subject != "moox.storage.fields_changed.v1.>" {
		t.Fatalf("checked-in nats subject = %q", cfg.NATS.Subject)
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
	t.Setenv("MOOX_FACTOR_NATS_URL", "")
	t.Setenv("MOOX_FACTOR_ENGINE_PYTHON_BIN", "/tmp/factor-python")
	t.Setenv("MOOX_FACTOR_HEALTH_ADDR", "127.0.0.1:16014")

	path := writeConfig(t, `
database:
  path: ./original/factor.db
nats:
  url: nats://127.0.0.1:4222
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.Path != "./override/factor.db" {
		t.Fatalf("database path = %q", cfg.Database.Path)
	}
	if cfg.NATS.URL != "" {
		t.Fatalf("nats url = %q, want empty override", cfg.NATS.URL)
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

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
