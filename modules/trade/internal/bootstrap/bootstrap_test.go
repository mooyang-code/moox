package bootstrap

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"gopkg.in/yaml.v3"
)

func TestTRPCConfigContainsOnlyApprovedServices(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Server struct {
			Service []struct {
				Name string `yaml:"name"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(config.Server.Service))
	for _, service := range config.Server.Service {
		names = append(names, service.Name)
	}
	sort.Strings(names)
	want := []string{
		"trpc.moox.trade.ExchangeAccountService",
		"trpc.moox.trade.Health",
		"trpc.moox.trade.TradeExecutionService",
		"trpc.moox.trade.metrics.timer",
	}
	if len(names) != len(want) {
		t.Fatalf("service names = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("service names = %v, want %v", names, want)
		}
	}
}

func TestExchangeCredentialPropagatesSingleSecretReadError(t *testing.T) {
	readErr := errors.New("secret service unavailable")
	secrets := &bootstrapSecretSource{err: readErr}

	_, err := exchangeCredential(
		context.Background(), secrets, exchange.ExchangeBinance, "secret-1",
	)
	if !errors.Is(err, readErr) {
		t.Fatalf("exchangeCredential() error = %v", err)
	}
	if secrets.getCalls != 1 {
		t.Fatalf("GetExchangeSecret() calls = %d, want 1", secrets.getCalls)
	}
}

func TestRegisterBuiltinsBindsSupportedExchanges(t *testing.T) {
	registry := exchange.NewRegistry()
	registerBuiltins(registry)
	for _, name := range []exchange.Exchange{
		exchange.ExchangeBinance,
		exchange.ExchangeOKX,
	} {
		adapter, err := registry.Bind(exchange.AccountConfig{
			ExchangeAccountID: "account-1",
			Exchange:          name,
			MarketType:        exchange.MarketTypeSpot,
			ExecutionMode:     exchange.ExecutionModePaper,
			SettlementAsset:   "USDT",
		}, exchange.Credential{})
		if err != nil {
			t.Fatalf("Bind(%s) error = %v", name, err)
		}
		if adapter.Exchange() != name {
			t.Fatalf("Bind(%s) adapter Exchange = %s", name, adapter.Exchange())
		}
	}

	_, err := registry.Bind(exchange.AccountConfig{
		ExchangeAccountID: "account-1",
		Exchange:          exchange.ExchangeOKX,
		MarketType:        exchange.MarketTypeSpot,
		ExecutionMode:     exchange.ExecutionModeLive,
		SettlementAsset:   "USDT",
	}, exchange.Credential{APIKey: "key", APISecret: "secret"})
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("OKX LIVE Bind() error = %v, want passphrase rejection", err)
	}
}

type bootstrapSecretSource struct {
	secret   accountapp.ExchangeSecret
	err      error
	getCalls int
}

func (s *bootstrapSecretSource) GetExchangeSecret(
	context.Context,
	string,
) (accountapp.ExchangeSecret, error) {
	s.getCalls++
	return s.secret, s.err
}

func TestBootstrapOwnsOneStoreAndOneShutdownHook(t *testing.T) {
	raw, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if got := strings.Count(source, "store.Open("); got != 1 {
		t.Fatalf("store.Open calls = %d, want 1", got)
	}
	if got := strings.Count(source, "RegisterOnShutdown("); got != 1 {
		t.Fatalf("shutdown hooks = %d, want 1", got)
	}
	for _, forbidden := range []string{
		"database.NewManager", "dao.New(", "RegisterAccountSvc",
		"RunRebalance", "fill_reconcile.timer", "order_recovery.timer",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("bootstrap still contains obsolete symbol %q", forbidden)
		}
	}
}
