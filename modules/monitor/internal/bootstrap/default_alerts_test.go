package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func TestEnsureDefaultCheckAlertRulesIsIdempotent(t *testing.T) {
	t.Setenv("MOOX_MSGBOX_WECOM_WEBHOOK", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	ctx := context.Background()
	for _, check := range []domain.Check{
		{CheckID: "sysdeploy:node-a:moox_collector", Kind: domain.CheckKindHTTP, Source: domain.CheckSourceSysDeploy, Enabled: true},
		{SpaceID: "crypto", CheckID: "market_canary:kline:BTC-USDT:1m", Kind: domain.CheckKindExternal, Source: domain.CheckSourceManual, Enabled: true},
	} {
		check := check
		if err := repositories.Checks.Create(ctx, &check); err != nil {
			t.Fatal(err)
		}
	}

	if err := ensureDefaultCheckAlertRules(ctx, repositories); err != nil {
		t.Fatal(err)
	}
	if err := ensureDefaultCheckAlertRules(ctx, repositories); err != nil {
		t.Fatal(err)
	}

	serviceRule, err := repositories.Alerts.GetRule(ctx, "", "default:sysdeploy:node-a:moox_collector")
	if err != nil {
		t.Fatal(err)
	}
	if serviceRule.FailureThreshold != 3 || serviceRule.SuccessThreshold != 2 {
		t.Fatalf("service rule = %+v", serviceRule)
	}
	canaryRule, err := repositories.Alerts.GetRule(ctx, "crypto", "default:market_canary:kline:BTC-USDT:1m")
	if err != nil {
		t.Fatal(err)
	}
	if canaryRule.FailureThreshold != 1 || canaryRule.SuccessThreshold != 1 {
		t.Fatalf("canary rule = %+v", canaryRule)
	}
}

func TestEnsureDefaultHostAlertRulesCoversRegisteredAgent(t *testing.T) {
	t.Setenv("MOOX_MSGBOX_WECOM_WEBHOOK", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	registry, err := store.WithDatabase(manager, hostmetrics.NewRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Observe(t.Context(), hostmetrics.HostObservation{
		AgentID: "agent-a", Hostname: "host-a", BootID: "boot-a", EventID: "event-a", OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureDefaultHostAlertRules(t.Context(), repositories, registry); err != nil {
		t.Fatal(err)
	}
	rules, err := repositories.Alerts.ListRules(t.Context(), hostmetrics.SpaceID)
	if err != nil {
		t.Fatal(err)
	}
	var hostRules int
	for _, rule := range rules {
		if _, _, ok := hostmetrics.ParseHostRuleKey(rule.CheckID); ok {
			hostRules++
			if rule.FailureThreshold != 20 {
				t.Fatalf("host failure threshold = %d", rule.FailureThreshold)
			}
		}
	}
	if hostRules != 5 {
		t.Fatalf("host rules = %d, want 5", hostRules)
	}
}
