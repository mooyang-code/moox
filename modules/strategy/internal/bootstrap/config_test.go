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
	if cfg.Trade.Timeout != defaultLogicalAccountTimeout {
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

func TestOfflineConfigDoesNotOpenGatewayIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("expected offline config without signing identity to load: %v", err)
	}
}

func TestLoadDerivesStorageAppKeysFromRuntimeSecrets(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "view-secret")
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\nstorage:\n  app_id: strategy\n"), 0o600); err != nil {
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

func TestOpenGatewayRejectsStorageWithoutAppKey(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "")
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.OpenGateway(nil); err == nil {
		t.Fatal("expected configured storage without app key to fail")
	}
}

func TestOpenGatewayRejectsStorageWithoutViewAppKey(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "")
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("database: ./strategy.sqlite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.OpenGateway(nil); err == nil {
		t.Fatal("expected configured storage without view app key to fail")
	}
}

func TestLoadRejectsInvalidLogicalAccountTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(
		path,
		[]byte("trade:\n  timeout: -1s\n"),
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
		Trade: TradeConfig{Timeout: time.Second},
	}, nil)
	if service.LogicalAccounts == nil {
		t.Fatalf("service=%+v", service)
	}
}

func TestLoadIgnoresRemovedTradeRoutingEnvironment(t *testing.T) {
	for _, key := range []string{"MOOX_TRADE_GATEWAY_URL", "MOOX_TRADE_GATEWAY_NODE_ID", "MOOX_TRADE_GATEWAY_CA_FILE", "MOOX_GATEWAY_CA_FILE"} {
		t.Setenv(key, "removed-routing-input")
	}
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("trade:\n  timeout: 2s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Trade.Timeout != 2*time.Second || cfg.GatewayClient.Caller != "strategy" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestStrategyConfigRejectsOldOrInvalidGatewayFields(t *testing.T) {
	for _, raw := range []string{
		"trade:\n  gateway_url: https://trade.example\n",
		"trade:\n  target_node: trade\n",
		"trade:\n  ca_file: ca.crt\n",
		"factor:\n  target: ip://127.0.0.1:11003\n",
		"storage:\n  target_node: storage\n",
		"storage:\n  target: ip://127.0.0.1:11003\n",
		"storage:\n  timeout: 5s\n",
		"gateway_client:\n  target: ip://127.0.0.1:11003\n",
		"gateway_client:\n  caller: factor-mgr\n",
		"gateway_client:\n  key_id: first\n  key_id: second\n",
		"database: ./strategy.sqlite\n---\ndatabase: ignored\n",
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.yaml")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	if _, err := (Config{}).OpenGateway(nil); err == nil {
		t.Fatal("gateway opened without a loaded configuration")
	}
}
