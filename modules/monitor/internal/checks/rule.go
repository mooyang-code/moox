package checks

import (
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/kline"
)

// Rule 是一项检查的告警规则，由检查 ID 推出：连续失败 Failures 次触发，告警中连续通过 Successes 次恢复，
// 告警持续时每 Reminder 提醒一次。
type Rule struct {
	Failures, Successes int
	Reminder            time.Duration
}

// ReminderInterval 是告警持续时的提醒间隔。
const ReminderInterval = 5 * time.Minute

// evaluationInterval 是检查定时器的间隔：除 K 线外的检查每 30 秒评估一次。
const evaluationInterval = 30 * time.Second

// alertGrace 是外部检查从第一次失败到告警的宽限时间。检查每个周期评估一次，服务重启时会有一两个周期读不到指标；
// 失败一次就告警会让一次重启推送一对“新告警 / 已恢复”（2026-10-14～15 两天 122 次告警，大多 29 秒内自愈）。
// 持续失败满 60 秒才告警，真正的故障最多晚一分钟。
const alertGrace = 60 * time.Second

// graceFailures 把宽限时间换算成连续失败次数：首次失败之后还要再失败满宽限时间。
func graceFailures(interval time.Duration) int {
	if interval <= 0 {
		interval = evaluationInterval
	}
	return 1 + int((alertGrace+interval-1)/interval)
}

// RuleFor 返回检查的告警规则。klineInterval 是 K 线新鲜度的评估间隔（不大于 0 时按 30 秒）。
//   - 主机失联、host-agent 停报：在采集器停报满 3 分钟后才产生失败，这 3 分钟就是宽限，失败一次即告警；
//   - 主机资源：按指标要求的连续采样次数告警；
//   - K 线新鲜度：按评估间隔换算 60 秒宽限，恢复要连续通过 2 次；
//   - 其余：按 30 秒间隔换算 60 秒宽限（3 次），恢复通过 1 次。
func RuleFor(checkID string, klineInterval time.Duration) Rule {
	rule := Rule{Failures: graceFailures(evaluationInterval), Successes: 1, Reminder: ReminderInterval}
	if _, kind, ok := hostmetrics.ParseCheckID(checkID); ok {
		rule.Failures = 1
		if samples := hostmetrics.MetricThreshold(kind).Samples; samples > 0 {
			rule.Failures = samples
		}
		return rule
	}
	if strings.HasPrefix(checkID, "kline_freshness:") {
		rule.Failures, rule.Successes = graceFailures(klineInterval), 2
	}
	return rule
}

// Severity 返回 Monitor 自己产出的检查的级别（关注项不走这里，级别来自生产者），页面展示和推送都用它：
// critical 表示服务不可用、数据中断、主机失联；组件停止上报、未就绪、反复重启都算不可用；
// warning 表示还在工作但有风险：主机资源超过阈值、只有 host-agent 停止上报、采集任务清单过期、指标序列新增过快。
func Severity(checkID string) string {
	switch {
	case strings.HasPrefix(checkID, "metrics_ingest:"), checkID == kline.TaskResultInventoryCheckID:
		return domain.SeverityWarning
	}
	if _, kind, ok := hostmetrics.ParseCheckID(checkID); ok && kind != hostmetrics.CheckKindPresence {
		return domain.SeverityWarning
	}
	return domain.SeverityCritical
}
