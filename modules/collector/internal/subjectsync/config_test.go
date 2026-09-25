package subjectsync

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subject.yaml")
	if err := os.WriteFile(path, []byte("storage:\n  target: 127.0.0.1:11003\nattributes:\n  - space_id: crypto\n    sources: [binance]\n    cron: \"0 0 * * *\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != time.Minute || cfg.FetchTimeout != 2*time.Minute || cfg.Attributes[0].Timezone != "UTC" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.HealthAddr != "127.0.0.1:11413" {
		t.Fatalf("health addr = %q", cfg.HealthAddr)
	}
}

func TestLoadConfigRejectsBadCron(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subject.yaml")
	if err := os.WriteFile(path, []byte("storage: {target: x}\nattributes: [{space_id: crypto, sources: [binance], cron: 'bad'}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("want error")
	}
}
