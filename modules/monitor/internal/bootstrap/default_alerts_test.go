package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events"
)

func TestEnsureDefaultCheckAlertRulesIsIdempotent(t *testing.T) {
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
		{SpaceID: "crypto", CheckID: "market_canary:kline:BTC-USDT:1m", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true},
		{SpaceID: "crypto", CheckID: "kline_freshness:crypto:view:1m", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true},
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

	canaryRule, err := repositories.Alerts.GetRule(ctx, "crypto", "default:market_canary:kline:BTC-USDT:1m")
	if err != nil {
		t.Fatal(err)
	}
	if canaryRule.FailureThreshold != 3 || canaryRule.SuccessThreshold != 1 {
		t.Fatalf("canary rule = %+v", canaryRule)
	}
	klineRule, err := repositories.Alerts.GetRule(ctx, "crypto", "default:kline_freshness:crypto:view:1m")
	if err != nil {
		t.Fatal(err)
	}
	if klineRule.FailureThreshold != 3 || klineRule.SuccessThreshold != 2 || klineRule.MinimumReminderIntervalSeconds != 300 || !klineRule.SendOnResolved {
		t.Fatalf("kline rule = %+v", klineRule)
	}
}

// 主机类检查的默认规则：失联与 host-agent 停报失败一次即告警（停报满 3 分钟才会失败）；
// 资源检查按采样次数——CPU、磁盘约 3 分钟，内存约 5 分钟。恢复都只要成功一次。
func TestDefaultRulesForHostChecksKeepTheirOwnTiming(t *testing.T) {
	repositories := openMonitorTestRepositories(t)
	want := map[string]int{
		hostmetrics.CheckKindPresence: 1, hostmetrics.CheckKindAgent: 1,
		hostmetrics.HostMetricCPU: 3, hostmetrics.HostMetricMemory: 5, hostmetrics.HostMetricFilesystemUsage: 3,
	}
	for kind := range want {
		check := &domain.Check{SpaceID: events.SystemSpaceID, CheckID: hostmetrics.CheckID("agent-a", kind), Name: kind, GroupName: "business",
			Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30, TimeoutMS: 20000}
		if err := repositories.Checks.Create(t.Context(), check); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureDefaultCheckAlertRules(t.Context(), repositories); err != nil {
		t.Fatal(err)
	}
	for kind, failures := range want {
		rule, err := repositories.Alerts.GetRule(t.Context(), events.SystemSpaceID, "default:"+hostmetrics.CheckID("agent-a", kind))
		if err != nil {
			t.Fatal(err)
		}
		if rule.FailureThreshold != failures || rule.SuccessThreshold != 1 || !rule.SendOnResolved || rule.MinimumReminderIntervalSeconds != 300 {
			t.Fatalf("%s rule = %+v", kind, rule)
		}
	}
}

// 外部检查持续失败满 60 秒才告警：首次失败之后还要再失败覆盖整个宽限时间。
func TestExternalFailureThresholdCoversGracePeriod(t *testing.T) {
	for interval, want := range map[int]int{30: 3, 10: 7, 60: 2, 45: 3, 120: 2, 0: 3} {
		if got := externalFailureThreshold(interval); got != want {
			t.Fatalf("externalFailureThreshold(%d) = %d, want %d", interval, got, want)
		}
	}
}
