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
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test")
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
		{CheckID: "placement:node-a:collector", Kind: domain.CheckKindHTTP, Source: domain.CheckSourcePlacement, Enabled: true},
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

	serviceRule, err := repositories.Alerts.GetRule(ctx, "", "default:placement:node-a:collector")
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

func TestEnsureDefaultHostAlertRulesCoversRegisteredAgent(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test")
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
	// 存量库里已精简掉的主机告警（磁盘繁忙度、网络错误包）必须被清理。
	for _, retired := range []string{"disk_utilization", "network_errors"} {
		checkID := "host:agent-a:" + retired
		if err := repositories.Alerts.CreateRule(t.Context(), &domain.AlertRule{SpaceID: hostmetrics.SpaceID, RuleID: "default:" + checkID, CheckID: checkID, FailureThreshold: 20, SuccessThreshold: 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureDefaultHostAlertRules(t.Context(), repositories, registry); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"disk_utilization", "network_errors"} {
		if _, err := repositories.Alerts.GetRule(t.Context(), hostmetrics.SpaceID, "default:host:agent-a:"+retired); err == nil {
			t.Fatalf("retired host rule %s was not removed", retired)
		}
	}
	rules, err := repositories.Alerts.ListRules(t.Context(), hostmetrics.SpaceID)
	if err != nil {
		t.Fatal(err)
	}
	var hostRules int
	for _, rule := range rules {
		if _, _, ok := hostmetrics.ParseHostRuleKey(rule.CheckID); ok {
			hostRules++
			_, metric, _ := hostmetrics.ParseHostRuleKey(rule.CheckID)
			// HostAgent 每分钟采样：CPU、磁盘约 3 分钟，内存约 5 分钟。
			wantFailureThreshold := map[string]int{hostmetrics.HostMetricCPU: 3, hostmetrics.HostMetricMemory: 5, hostmetrics.HostMetricFilesystemUsage: 3}[metric]
			if rule.FailureThreshold != wantFailureThreshold {
				t.Fatalf("host %s failure threshold = %d, want %d", metric, rule.FailureThreshold, wantFailureThreshold)
			}
			if metric == hostmetrics.HostMetricFilesystemUsage && rule.Description != `{"threshold":85,"recovery_threshold":80}` {
				t.Fatalf("filesystem alert definition = %s", rule.Description)
			}
			// CPU 的阈值要能抓住“某个进程把几核主机吃到八九成”这种持续高负载：80%、约 3 分钟。
			if metric == hostmetrics.HostMetricCPU && rule.Description != `{"threshold":80,"recovery_threshold":70}` {
				t.Fatalf("cpu alert definition = %s", rule.Description)
			}
			if metric == hostmetrics.HostMetricMemory && rule.Description != `{"threshold":90,"recovery_threshold":80}` {
				t.Fatalf("memory alert definition = %s", rule.Description)
			}
		}
	}
	if hostRules != 3 {
		t.Fatalf("host rules = %d, want 3 (cpu, memory, filesystem_usage)", hostRules)
	}
}

// 部署配置里的推送地址只写入一次（启动时只在首次建库调用）；没有配置地址时不写入渠道。
func TestSeedNotificationChannelAddsDeploymentChannel(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	if err := seedNotificationChannel(t.Context(), repositories); err != nil {
		t.Fatal(err)
	}
	if channels, err := repositories.Notifications.List(t.Context()); err != nil || len(channels) != 0 {
		t.Fatalf("empty deployment URL must not add a channel: %+v err=%v", channels, err)
	}
	const url = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=seed"
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", url)
	if err := seedNotificationChannel(t.Context(), repositories); err != nil {
		t.Fatal(err)
	}
	channels, err := repositories.Notifications.List(t.Context())
	if err != nil || len(channels) != 1 || channels[0].ChannelType != "wecom" || channels[0].WebhookURL != url {
		t.Fatalf("channels = %+v err=%v", channels, err)
	}
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "https://not-approved.example/hook")
	if err := seedNotificationChannel(t.Context(), repositories); err == nil {
		t.Fatal("an invalid deployment URL must fail the first start")
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
