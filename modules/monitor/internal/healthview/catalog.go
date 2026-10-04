package healthview

import "strings"

type Definition struct{ Key, Name, Remark string }

var businessCatalog = []Definition{
	{Key: "market", Name: "行情采集", Remark: "检查 K 线采集、数据完整性、更新延迟和 SCF 容量"},
	{Key: "factor", Name: "因子计算", Remark: "检查计算失败、输入输出时间差和无效结果"},
	{Key: "trade", Name: "交易与余额", Remark: "检查账户余额同步、余额差异和交易服务状态"},
}

func ChineseName(id string) (string, string) {
	id = strings.ToLower(id)
	switch {
	case strings.Contains(id, "collector"), strings.Contains(id, "market_fetch"), strings.Contains(id, "canary"):
		return "行情采集", "检查行情任务分配、Timer 容量和 K 线新鲜度"
	case strings.Contains(id, "factor"):
		return "因子计算", "检查因子计算失败、时序滞后和结果新鲜度"
	case strings.Contains(id, "balance"), strings.Contains(id, "trade"):
		return "交易与余额", "检查账户余额同步和交易相关业务状态"
	case strings.Contains(id, "dataset"), strings.Contains(id, "storage_view"), strings.Contains(id, "storage-view"), strings.Contains(id, "storage:"):
		return "数据与视图", "检查数据集和视图的最新水位"
	default:
		return "核心服务", "检查服务是否在线并持续上报"
	}
}

func Status(status string) string {
	switch status {
	case "healthy", "ok":
		return "healthy"
	case "degraded", "stale":
		return "degraded"
	case "down", "firing":
		return "down"
	default:
		return "unknown"
	}
}

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
	switch text {
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
