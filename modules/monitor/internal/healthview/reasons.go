package healthview

import "strings"

func MaskURL(raw string) string {
	if raw == "" {
		return ""
	}
	if len(raw) <= 12 {
		return "********"
	}
	return raw[:8] + "..." + raw[len(raw)-4:]
}

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
	case "reporter missing":
		return "监控上报已中断"
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
			return "监控检查失败，请查看日志详情"
		}
		return text
	}
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	return true
}
