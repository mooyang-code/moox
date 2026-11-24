package checks

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/notification"
)

// messageProcessLimit 是推送正文里列出的进程数；事件里保留完整的排行。
const messageProcessLimit = 3

// message 是推送正文：每行一件事，按人需要的顺序排；检查名是标题。
func message(eventType string, row domain.Check, reason, diagnostic string, at time.Time) string {
	reason = alerttext.Reason(strings.TrimSpace(reason))
	var lines []string
	switch eventType {
	case domain.AlertEventResolved:
		line := "状态：已恢复"
		if since := alertDuration(row, at); since != "" {
			line += "（持续了 " + since + "）"
		}
		lines = append(lines, line)
		if reason != "" {
			lines = append(lines, "当前："+reason)
		}
	case domain.AlertEventReminder:
		line := "状态：仍未恢复"
		if since := alertDuration(row, at); since != "" {
			line += "（已持续 " + since + "）"
		}
		lines = append(lines, line)
		if reason != "" {
			lines = append(lines, "问题："+reason)
		}
	case domain.AlertEventSendFailed:
		lines = append(lines, "状态：告警通知发送失败")
		if reason != "" {
			lines = append(lines, "问题："+reason)
		}
	default:
		lines = append(lines, "状态：新告警")
		if reason != "" {
			lines = append(lines, "问题："+reason)
		}
	}
	if eventType == domain.AlertEventTriggered || eventType == domain.AlertEventReminder {
		detail := alerttext.DecodeProcessDetail(diagnostic)
		if top := alerttext.TopProcesses(detail.TopProcesses, detail.ByMemory(), messageProcessLimit); top != "" {
			// 整机的使用率是全部核的百分比，进程的 CPU 按单核计（和 top 一样），这里注明，免得两个数对不上。
			lines = append(lines, "占用最高（进程 CPU 按单核计）："+top)
		}
	}
	if !at.IsZero() {
		lines = append(lines, "时间："+alerttext.Time(at)+"（北京时间）")
	}
	return strings.Join(lines, "\n")
}

// alertDuration 是告警已持续多久，例如“1 小时 5 分钟”；不知道时为空。
func alertDuration(row domain.Check, now time.Time) string {
	if row.TriggeredAt == nil || now.IsZero() || now.Before(*row.TriggeredAt) {
		return ""
	}
	return alerttext.Duration(now.Sub(*row.TriggeredAt))
}

// payload 是事件的结构化内容：问题描述、机器诊断；主机 CPU、内存检查另带当时的进程排行（进程事后退出或被杀掉，仍能查到当时是谁占的）。
// 恢复事件只带原因。
func payload(eventType, reason, diagnostic string) string {
	fields := struct {
		Reason     string `json:"reason"`
		Diagnostic string `json:"diagnostic,omitempty"`
		alerttext.ProcessDetail
	}{Reason: alerttext.Reason(strings.TrimSpace(reason))}
	if eventType != domain.AlertEventResolved {
		fields.ProcessDetail = alerttext.DecodeProcessDetail(diagnostic)
		if fields.Metric == "" && len(fields.TopProcesses) == 0 {
			fields.Diagnostic = diagnostic
		}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// notificationSeverity 是推送的级别：新告警和提醒用检查自己的级别（与页面一致），恢复通知是提示。
func notificationSeverity(eventType, severity string) notification.Severity {
	if eventType == domain.AlertEventResolved {
		return notification.SeverityInfo
	}
	if severity == domain.SeverityWarning {
		return notification.SeverityWarning
	}
	return notification.SeverityCritical
}
