package checks

import (
	"testing"
	"time"
)

// 期望值按改前的 ensureDefaultRules、hostCheckFailureThreshold 与 alerting.Severity 逐项算出：规则与级别改成纯函数后结果不变。
func TestRuleForAndSeverityMatchPreviousDefaults(t *testing.T) {
	for _, test := range []struct {
		checkID             string
		klineInterval       time.Duration
		failures, successes int
		severity            string
	}{
		{"host:control:presence", 0, 1, 1, "critical"},
		{"host:control:agent", 0, 1, 1, "warning"},
		{"host:control:cpu", 0, 3, 1, "warning"},
		{"host:control:memory", 0, 5, 1, "warning"},
		{"host:control:filesystem_usage", 0, 3, 1, "warning"},
		{"kline_freshness:crypto:ds:1m", 30 * time.Second, 3, 2, "critical"},
		{"kline_freshness:crypto:ds:1m", 60 * time.Second, 2, 2, "critical"},
		{"kline_freshness:crypto:ds:1m", 15 * time.Second, 5, 2, "critical"},
		{"kline_freshness:crypto:ds:1m", 0, 3, 2, "critical"},
		{"kline_freshness:task_result_inventory", 30 * time.Second, 3, 2, "warning"},
		{"reporter:control:collector", 0, 3, 1, "critical"},
		{"ready:control:collector", 0, 3, 1, "critical"},
		{"restart:control:collector", 0, 3, 1, "critical"},
		{"dataset:storage:ds:1m", 0, 3, 1, "critical"},
		{"module:collector_fetch", 0, 3, 1, "critical"},
		{"market_canary:BTC-USDT:1m", 0, 3, 1, "critical"},
		{"subject_tag:crypto:binance_spot", 0, 3, 1, "critical"},
		{"observation:ingest", 0, 3, 1, "critical"},
		{"metrics_ingest:series_limit", 0, 3, 1, "warning"},
		{"archive_coverage:archive", 0, 3, 1, "critical"},
		{"attention:trade:auto_paused", 0, 3, 1, "critical"},
		// 不是合法主机检查的 host: 前缀按普通检查算。
		{"host:control:unknown", 0, 3, 1, "critical"},
	} {
		rule := RuleFor(test.checkID, test.klineInterval)
		if rule.Failures != test.failures || rule.Successes != test.successes || rule.Reminder != 5*time.Minute {
			t.Errorf("%s（K 线间隔 %s）规则 %+v，应为 %d/%d", test.checkID, test.klineInterval, rule, test.failures, test.successes)
		}
		if got := Severity(test.checkID); got != test.severity {
			t.Errorf("%s 级别 %s，应为 %s", test.checkID, got, test.severity)
		}
	}
}

func TestDomainOf(t *testing.T) {
	for checkID, want := range map[string]Domain{
		"host:control:presence":           DomainHost,
		"host:control:cpu":                DomainHost,
		"market_canary:BTC-USDT:1m":       DomainCanary,
		"subject_tag:crypto:binance_spot": DomainCanary,
		"reporter:control:collector":      DomainBusiness,
		"gateway:control:status":          DomainBusiness,
		"attention:trade:auto_paused":     DomainBusiness,
	} {
		if got := DomainOf(checkID); got != want {
			t.Errorf("%s 属于域 %d，应为 %d", checkID, got, want)
		}
	}
}
