package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
)

func TestLoadAppliesSafeDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InstanceID != "strategy-1" || cfg.EventBus.RelayInterval != time.Second || cfg.EventBus.RelayBatchSize != 100 || cfg.EventBus.ConnectTimeout != 3*time.Second {
		t.Fatalf("eventbus defaults=%+v", cfg)
	}
	if cfg.LogicalAccountTarget != "ip://127.0.0.1:11200" ||
		cfg.LogicalAccountTimeout != defaultLogicalAccountTimeout {
		t.Fatalf("logical account config=%+v", cfg)
	}
}

func TestDefaultPoolRegistryDeduplicatesMultiVenueSymbols(t *testing.T) {
	registry := defaultPoolRegistry()
	ids, err := registry.Resolve(context.Background(), config.Pool{UDF: &config.PoolUDF{Name: "all_symbols"}}, []input.Subject{
		{InstrumentID: "BTCUSDT", Exchange: "binance", Active: true},
		{InstrumentID: "btcusdt", Exchange: "okx", Active: true},
		{InstrumentID: "ETHUSDT", Exchange: "binance", Active: true},
	}, time.UnixMilli(1))
	if err != nil {
		t.Fatalf("resolve built-in pool: %v", err)
	}
	if got, want := len(ids), 2; got != want || ids[0] != "BTCUSDT" || ids[1] != "ETHUSDT" {
		t.Fatalf("ids=%v, want [%s %s]", ids, "BTCUSDT", "ETHUSDT")
	}
}

func TestLoadRejectsInvalidEventBusRuntimeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\neventbus:\n  relay_interval: -1s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid EventBus settings to fail")
	}
}

func TestDeclarativeConfigMayOmitDependencyTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("expected declarative config without worker path to load: %v", err)
	}
}

func TestLoadDerivesStorageAppKeysFromRuntimeSecrets(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "view-secret")
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\nstorage:\n  target: ip://storage:11003\n  app_id: strategy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.AppKey != serviceAuthKey("primary-secret", "strategy") {
		t.Fatalf("storage app key = %q", cfg.Storage.AppKey)
	}
	if cfg.Storage.ViewAppKey != serviceAuthKey("view-secret", "strategy") {
		t.Fatalf("storage view app key = %q", cfg.Storage.ViewAppKey)
	}
}

func TestLoadRejectsConfiguredStorageWithoutAppKey(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "")
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\nstorage:\n  target: ip://storage:11003\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected configured storage without app key to fail")
	}
}

func TestLoadRejectsConfiguredStorageWithoutViewAppKey(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "")
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\nstorage:\n  target: ip://storage:11003\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected configured storage without view app key to fail")
	}
}

func TestLoadRejectsInvalidLogicalAccountTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(
		path,
		[]byte("logical_account_timeout: -1s\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid LogicalAccount timeout to fail")
	}
}

func TestNewRPCServiceUsesLogicalAccountOwnerClient(t *testing.T) {
	service := newRPCService(nil, Config{
		LogicalAccountTarget: "ip://trade:11200",
	})
	if service.LogicalAccounts == nil {
		t.Fatalf("service=%+v", service)
	}
}
