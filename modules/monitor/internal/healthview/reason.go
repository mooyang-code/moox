package healthview

import (
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
)

// normalizeStatus 把各来源的状态统一成健康概览的状态值。
func normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "healthy", "ok":
		return StatusHealthy
	case "degraded", "stale":
		return StatusDegraded
	case "down", "firing":
		return StatusDown
	case StatusDisabled:
		return StatusDisabled
	case StatusUnchecked:
		return StatusUnchecked
	default:
		return StatusUnknown
	}
}

// statusRank 越大越严重；用于取一组状态中最严重的一个。
func statusRank(status string) int {
	switch status {
	case StatusDown:
		return 5
	case StatusDegraded:
		return 4
	case StatusUnknown:
		return 3
	case StatusHealthy:
		return 2
	case StatusUnchecked:
		return 1
	default:
		return 0
	}
}

func worst(statuses ...string) string {
	out := ""
	for _, status := range statuses {
		if out == "" || statusRank(status) > statusRank(out) {
			out = status
		}
	}
	return out
}

// MaskURL 返回 Webhook 地址的掩码。
func MaskURL(raw string) string {
	if raw == "" {
		return ""
	}
	if len(raw) <= 12 {
		return "********"
	}
	return raw[:8] + "..." + raw[len(raw)-4:]
}

// ChineseReason 把技术性的原因翻译成中文摘要；已经是中文的原因原样返回。
func ChineseReason(raw string) string {
	text := strings.TrimSpace(raw)
	if strings.Contains(text, ";") {
		parts := strings.Split(text, ";")
		translated := make([]string, 0, len(parts))
		for _, part := range parts {
			if value := ChineseReason(strings.TrimSpace(part)); value != "" {
				translated = append(translated, value)
			}
		}
		return strings.Join(translated, "；")
	}
	if base, ok := strings.CutSuffix(text, ": authentication failed"); ok {
		return ChineseReason(base) + "（认证失败）"
	}
	switch text {
	case "eventbus connection unavailable":
		return "无法连接消息总线"
	case "metrics history write to Storage failed":
		return "指标历史写入存储服务失败"
	case "metrics catalog write failed":
		return "指标目录写入监控数据库失败"
	case "host metrics write failed":
		return "主机指标写入失败"
	case "reporter fresh":
		return "监控上报正常"
	case "producer stale":
		return "数据生产端长时间未更新"
	case "health not checked":
		return "尚未完成健康检查"
	case "health check failed":
		return "健康检查失败"
	case "health check degraded":
		return "健康检查需要关注"
	case "health check ok":
		return "健康检查正常"
	case "agent reachable":
		return "主机连接正常"
	case "agent unreachable":
		return "主机无法连接"
	case "balance sync fresh":
		return "账户余额同步正常"
	case "balance sync stale":
		return "账户余额同步延迟"
	case "balance sync failed 3 consecutive runs":
		return "账户余额已连续三次同步失败"
	case "run stale":
		return "任务运行结果已过期"
	case "success stale":
		return "最近成功结果已过期"
	case "inventory_stale":
		return "资源清单已过期"
	case "check failed":
		return "检查失败"
	case "normal":
		return "正常"
	case "unknown":
		return "未知"
	default:
		if strings.HasPrefix(text, "balance difference ") {
			values := strings.Fields(strings.TrimPrefix(text, "balance difference "))
			if len(values) == 3 && values[1] == "exceeds" {
				return "账户余额差异超过阈值（当前值 " + values[0] + "，阈值 " + values[2] + "）"
			}
			return "账户余额差异超过阈值"
		}
		if text != "" && isASCII(text) {
			return "检查失败，详见原始错误"
		}
		return text
	}
}

// rawError 返回需要单独展示的原始错误：中文摘要与原文相同时不重复展示。
func rawError(raw, summary string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == summary {
		return ""
	}
	return raw
}

// probeReason 是探测失败的中文摘要。
func probeReason(raw string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "健康检查失败"
	case strings.Contains(raw, "context deadline exceeded") || strings.Contains(raw, "Client.Timeout"):
		return "健康检查超时：服务未能在规定时间内返回就绪状态"
	case strings.Contains(raw, "connection refused"):
		return "健康检查失败：服务端口拒绝连接"
	case strings.Contains(raw, "no route to host") || strings.Contains(raw, "i/o timeout"):
		return "健康检查失败：无法连接到主机"
	}
	return ChineseReason(raw)
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	return true
}

// datasetReason 返回数据集状态的中文摘要和原始错误：已知的原因码由摘要完整说明，不再作为原始错误展示。
func datasetReason(item observability.DatasetFrequencyStatus) (string, string) {
	switch {
	case item.Status == "healthy" || item.Status == "ok":
		return "数据按时更新", ""
	case item.Reason == "尚未上报" || item.LastRunAt.IsZero():
		return "尚未收到运行上报，请检查任务是否启动、消费队列是否正常", ""
	case item.Reason == "producer stale":
		return "生产端的监控上报已中断，最近上报 " + alerttext.Time(item.LastReportedAt), ""
	case strings.Contains(item.Reason, "输出水位已落后"):
		return item.Reason, ""
	case item.Reason == "run stale" || item.Reason == "success stale":
		reason := "已超过允许时间未更新"
		if !item.LastSuccessAt.IsZero() {
			reason += "；最近成功 " + alerttext.Time(item.LastSuccessAt)
		}
		if item.LagSeconds > 0 {
			reason += "；当前落后 " + alerttext.Duration(time.Duration(item.LagSeconds)*time.Second)
		}
		return reason, ""
	}
	reason := ChineseReason(item.Reason)
	return reason, rawError(item.Reason, reason)
}

// datasetCheckParts 解析数据集检查 ID dataset:<生产者>:<数据集>:<频率>。
func datasetCheckParts(checkID string) (producer, datasetID, freq string, ok bool) {
	parts := strings.Split(strings.TrimSpace(checkID), ":")
	if len(parts) < 4 || parts[0] != "dataset" {
		return "", "", "", false
	}
	producer = strings.TrimSpace(parts[1])
	freq = strings.TrimSpace(parts[len(parts)-1])
	datasetID = strings.TrimSpace(strings.Join(parts[2:len(parts)-1], ":"))
	return producer, datasetID, freq, producer != "" && datasetID != "" && freq != ""
}

// isHostMonitoringDataset 判断是否是主机指标数据集；它们由主机告警覆盖，不放进数据链路。
func isHostMonitoringDataset(item observability.DatasetFrequencyStatus) bool {
	for _, id := range []string{item.DatasetID, item.PrimaryDatasetID} {
		id = strings.ToLower(strings.TrimSpace(id))
		if strings.HasPrefix(id, "dataset_mooxsys_host_") || strings.HasPrefix(id, "view_mooxsys_host_") {
			return true
		}
	}
	return false
}

func datasetScopeKey(spaceID, datasetID, freq string) string {
	return strings.Join([]string{strings.TrimSpace(spaceID), strings.TrimSpace(datasetID), strings.ToLower(strings.TrimSpace(freq))}, "\x00")
}

// collectorCoveredByStorage 判断 Collector 的数据集是否已有 Storage 的事实：Timer 采集不上报每个数据集的完成时间，
// 有 Storage 事实时以 Storage 为准，避免重复。
func collectorCoveredByStorage(item observability.DatasetFrequencyStatus, storageScopes map[string]struct{}) bool {
	if !strings.EqualFold(strings.TrimSpace(item.Producer), "collector") {
		return false
	}
	_, ok := storageScopes[datasetScopeKey(item.SpaceID, item.DatasetID, item.Freq)]
	return ok
}
