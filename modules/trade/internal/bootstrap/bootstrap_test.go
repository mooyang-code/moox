package bootstrap

import (
	"os"
	"sort"
	"strings"
	"testing"

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
