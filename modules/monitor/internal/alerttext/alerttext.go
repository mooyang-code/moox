// Package alerttext renders the facts alerts carry in plain Chinese, so the
// person reading a notification knows at once what broke and where.
package alerttext

import (
	"fmt"
	"strings"
	"time"
)

// Zone renders alert times in Beijing time, which operators read.
var Zone = time.FixedZone("CST", 8*60*60)

// Time renders a time for alert text, such as "10-07 20:15".
func Time(at time.Time) string {
	if at.IsZero() {
		return "未知"
	}
	return at.In(Zone).Format("01-02 15:04")
}

// Duration renders a duration for alert text, such as "1 小时 5 分钟".
func Duration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	minutes := int(d.Minutes())
	hours, minutes := minutes/60, minutes%60
	switch {
	case hours == 0:
		return fmt.Sprintf("%d 分钟", minutes)
	case hours >= 48:
		return fmt.Sprintf("%d 天 %d 小时", hours/24, hours%24)
	case minutes == 0:
		return fmt.Sprintf("%d 小时", hours)
	default:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
	}
}

// serviceNames names deployed services for people reading alerts.
var serviceNames = map[string]string{
	"admin_gateway":            "管理后台网关",
	"web_host":                 "管理后台前端",
	"service_gateway":          "服务网关",
	"service_gateway_native":   "公网服务网关",
	"moox_gateway":             "节点服务网关",
	"storage-primary":          "存储主服务",
	"storage-view":             "存储视图服务",
	"storage-node":             "存储数据节点",
	"eventbus":                 "消息总线",
	"moox_monitor":             "监控服务",
	"moox_collector":           "行情采集服务",
	"moox_collector_subject":   "采集对象同步服务",
	"collector_market_runtime": "行情采集运行环境",
	"moox_cloudnode":           "云节点服务",
	"moox_factor_mgr":          "因子管理服务",
	"moox_strategy":            "策略服务",
	"moox_archive":             "归档服务",
	"moox_hostagent":           "主机采集代理",
	"moox_trade":               "交易服务",
}

// Service is a deployed service's human name, falling back to its ID.
func Service(service string) string {
	if name := serviceNames[strings.TrimSpace(service)]; name != "" {
		return name
	}
	return service
}

// Node renders a node for alert text, such as "storage 节点".
func Node(node string) string {
	node = strings.TrimSpace(node)
	if node == "" {
		return "未知节点"
	}
	return node + " 节点"
}

// Frequency renders a frequency for alert text, such as "1小时".
func Frequency(freq string) string {
	freq = strings.TrimSpace(freq)
	if len(freq) < 2 {
		return freq
	}
	amount, unit := freq[:len(freq)-1], freq[len(freq)-1:]
	switch unit {
	case "s":
		return amount + "秒"
	case "m":
		return amount + "分钟"
	case "h", "H":
		return amount + "小时"
	case "d", "D":
		return amount + "天"
	case "w", "W":
		return amount + "周"
	case "M":
		return amount + "个月"
	default:
		return freq
	}
}

// Subjects renders up to five subject names, noting how many there are in
// total, such as "A、B、C 等 12 个".
func Subjects(names []string, total int) string {
	const shown = 5
	if len(names) == 0 {
		return ""
	}
	list := names
	if len(list) > shown {
		list = list[:shown]
	}
	text := strings.Join(list, "、")
	if total > len(list) {
		text += fmt.Sprintf(" 等 %d 个", total)
	}
	return text
}

// reasons translates the machine reason codes some checks report; checks
// that already report sentences pass through unchanged.
var reasons = map[string]string{
	"invalid_config":                      "探针配置无效，请检查监控配置",
	"subject_catalog_unavailable":         "无法读取采集对象清单",
	"storage_unreachable":                 "无法连接存储服务，读取不到行情数据",
	"insufficient_closed_bars":            "已收盘的K线数量不足，无法判断",
	"stale_watermark":                     "行情K线没有按时更新",
	"no_eligible_kline_feed":              "没有符合条件的K线采集来源",
	"calendar_unavailable":                "无法读取交易日历",
	"calendar_expired":                    "交易日历已过期，请更新",
	"calendar_expiring":                   "交易日历即将过期，请尽快更新",
	"source_provider_not_eligible":        "行情数据来源不符合要求",
	"no_closed_bar_data":                  "没有已收盘的K线数据",
	"closed_bar_coverage_below_threshold": "已收盘K线的覆盖率低于阈值，部分对象缺数据",
}

// Reason returns the plain Chinese text for a machine reason code, or the
// reason itself when it is not a known code.
func Reason(reason string) string {
	if text := reasons[strings.TrimSpace(reason)]; text != "" {
		return text
	}
	return reason
}
