package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAppliesSafeDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\nworker_path: ./worker.py\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PythonBin != "python3" || cfg.Workers != 1 || cfg.LiveEnabled {
		t.Fatalf("cfg=%+v", cfg)
	}
}

func TestLiveRequiresWorkerPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("live_enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected live worker path validation")
	}
}
