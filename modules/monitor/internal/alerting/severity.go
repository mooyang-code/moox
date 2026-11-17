package alerting

import (
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/packages/notification"
)

const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

// Severity 按检查 ID 给出告警的轻重，页面展示和推送都用它：
// critical 表示服务不可用、数据中断、主机失联或网关异常；组件健康只看上报（设计 3.8），组件停止上报、未就绪、反复重启都算不可用；
// warning 表示还在工作但有风险，例如主机资源超过阈值、只有 host-agent 停止上报、采集任务清单过期、
// 指标序列新增过快、只需提醒的关注项（缺币安只读 Key：现货下架改为手工登记）。
func Severity(checkID string) string {
	parts := strings.Split(checkID, ":")
	switch {
	case parts[0] == "metrics_ingest", checkID == monmetrics.TaskResultInventoryCheckID:
		return SeverityWarning
	case len(parts) == 2 && observability.AttentionCheckName(parts[0], parts[1]) != "" && observability.AttentionIsWarning(parts[0], parts[1]):
		return SeverityWarning
	}
	if _, kind, ok := hostmetrics.ParseCheckID(checkID); ok && kind != hostmetrics.CheckKindPresence {
		return SeverityWarning
	}
	return SeverityCritical
}

// notificationSeverity 是推送的级别：新告警和重复提醒用告警自己的级别（与页面一致），恢复通知是提示。
func notificationSeverity(eventType, checkID string) notification.Severity {
	if eventType == "resolved" {
		return notification.SeverityInfo
	}
	if Severity(checkID) == SeverityWarning {
		return notification.SeverityWarning
	}
	return notification.SeverityCritical
}
