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
	if cfg.PythonBin != "python3" || cfg.Workers != 1 {
		t.Fatalf("cfg=%+v", cfg)
	}
	if cfg.InstanceID != "strategy-1" || cfg.EventBus.RelayInterval != time.Second || cfg.EventBus.RelayBatchSize != 100 || cfg.EventBus.ConnectTimeout != 3*time.Second {
		t.Fatalf("eventbus defaults=%+v", cfg)
	}
	if cfg.LogicalAccountTarget != "ip://127.0.0.1:11202" ||
		cfg.LogicalAccountTimeout != defaultLogicalAccountTimeout {
		t.Fatalf("logical account config=%+v", cfg)
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

func TestWorkerPathIsAlwaysRequired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected worker path validation")
	}
}

func TestLoadRejectsInvalidLogicalAccountTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(
		path,
		[]byte("worker_path: ./worker.py\nlogical_account_timeout: -1s\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid LogicalAccount timeout to fail")
	}
}

func TestNewRPCServiceUsesLogicalAccountOwnerClient(t *testing.T) {
	service := newRPCService(nil, nil, Config{
		Workers:              2,
		LogicalAccountTarget: "ip://trade:11200",
	})
	if service.Workers != 2 || service.LogicalAccounts == nil || service.Results == nil {
		t.Fatalf("service=%+v", service)
	}
}
