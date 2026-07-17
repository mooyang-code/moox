package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if cfg.InstanceID != "strategy-1" || cfg.EventBus.RelayInterval != time.Second || cfg.EventBus.RelayBatchSize != 100 || cfg.EventBus.ConnectTimeout != 3*time.Second {
		t.Fatalf("eventbus defaults=%+v", cfg)
	}
}

func TestLoadRejectsInvalidEventBusRuntimeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\nworker_path: ./worker.py\neventbus:\n  relay_interval: -1s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid EventBus settings to fail")
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

func TestNewRPCServicePropagatesLiveCapability(t *testing.T) {
	if !newRPCService(nil, nil, Config{Workers: 2, LiveEnabled: true}).LiveExecutionEnabled {
		t.Fatal("live execution capability was not propagated")
	}
	if newRPCService(nil, nil, Config{Workers: 2, LiveEnabled: false}).LiveExecutionEnabled {
		t.Fatal("live execution capability must fail closed")
	}
}
