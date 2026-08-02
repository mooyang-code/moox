package alerting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/msgbox"
)

type WebhookNotifier struct {
	NewSender func(string) (msgbox.Sender, error)
	Timeout   time.Duration
	ReportIP  string
}

func (n WebhookNotifier) Send(ctx context.Context, webhook domain.WebhookChannel, event Event) error {
	factory := n.NewSender
	if factory == nil {
		factory = func(url string) (msgbox.Sender, error) {
			if n.Timeout > 0 {
				return msgbox.NewWeComSenderWithTimeout(url, n.Timeout)
			}
			return msgbox.NewWeComSender(url)
		}
	}
	sender, err := factory(webhook.URL)
	if err != nil {
		return err
	}
	severity := msgbox.SeverityCritical
	if event.EventType == domain.AlertEventResolved {
		severity = msgbox.SeverityInfo
	}
	return sender.Send(ctx, msgbox.Message{
		Key: event.DedupeKey, Severity: severity,
		Title: notificationTitle(event),
		Body:  notificationBody(event, firstText(n.ReportIP, os.Getenv("MOOX_REPORT_IP"))),
		Labels: map[string]string{
			"检查项": event.Check.CheckID, "事件": localizedEventType(event.EventType),
			"状态": localizedAlertStatus(event.Status), "实例": event.Result.InstanceID,
		},
	})
}

func notificationTitle(event Event) string {
	name := firstText(event.Check.Name, event.Check.CheckID, "MooX 监控")
	return localizedEventType(event.EventType) + "：" + name
}

func notificationBody(event Event, reportIP string) string {
	reason, action := localizedReason(event.Check, event.Result)
	if event.EventType == domain.AlertEventResolved {
		reason = "检查已恢复正常"
		action = ""
	}
	lines := []string{"异常原因：" + reason}
	if action != "" {
		lines = append(lines, "建议处理："+action)
	}
	if strings.HasPrefix(event.Check.CheckID, "external:scf_sentinel:") {
		if nodeID := safeNotificationValue(event.Result.InstanceID, 256); nodeID != "" {
			lines = append(lines, "SCF 节点："+nodeID)
		}
		if diagnostic := strings.TrimSpace(event.Result.BodyExcerpt); diagnostic != "" {
			lines = append(lines, "诊断信息："+diagnostic)
		}
		if resultID := strings.TrimSpace(event.Result.ResultID); resultID != "" {
			lines = append(lines, "诊断编号："+resultID)
		}
	} else {
		if reportIP = safeNotificationValue(reportIP, 128); reportIP != "" {
			lines = append(lines, "上报 IP："+reportIP)
		}
	}
	lines = append(lines,
		"检查对象："+firstText(event.Check.Name, event.Check.CheckID),
		"检查时间："+event.Result.CheckedAt.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02 15:04:05 MST"),
	)
	return strings.Join(lines, "\n")
}

func safeNotificationValue(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\t':
			return ' '
		default:
			return r
		}
	}, strings.TrimSpace(value))
	runes := []rune(value)
	if limit > 0 && len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func localizedReason(check domain.Check, result domain.CheckResult) (string, string) {
	raw := strings.TrimSpace(result.ErrorMessage)
	switch {
	case raw == "reporter missing" || raw == "reporter has not reported":
		service, target := reporterCheckTarget(check)
		return "未收到 " + target + " 的指标上报",
			fmt.Sprintf("检查 %s 的 Reporter 定时器、EventBus 连接，并确认上报节点 ID 与 SysDeploy 注册一致", service)
	case raw == "producer stale":
		service, target := reporterCheckTarget(check)
		return target + " 的指标上报已超过允许时间",
			fmt.Sprintf("检查 %s 进程、Reporter 定时器和 EventBus 连接", service)
	case strings.HasPrefix(raw, "storage_rejected_query"):
		parts := strings.SplitN(raw, ":", 3)
		reason := "Storage 拒绝了行情查询请求"
		if len(parts) >= 2 && parts[1] != "" {
			reason += "（返回码 " + parts[1]
			if len(parts) == 3 && parts[2] != "" {
				reason += "：" + parts[2]
			}
			reason += "）"
		}
		return reason,
			"检查该 Dataset、Symbol、Frequency 是否已启用且参数有效；若返回鉴权错误，再核对 Monitor 与 Storage Primary 的鉴权凭据"
	case raw == "storage_unreachable":
		return "Monitor 无法连接 Storage",
			"检查 Storage Primary 进程、服务路由和网络连通性"
	case raw == "insufficient_closed_bars":
		return "可用的已收盘 K 线不足两根",
			"检查采集任务是否持续写入该 Dataset、Symbol 和 Frequency"
	case raw == "stale_watermark":
		return "最新 K 线已经超过允许的新鲜度",
			"检查行情采集任务最近一次成功时间及 Dataset 最新数据时间"
	case raw == "threshold_exceeded":
		return "价格涨跌幅或成交量变化超过预设阈值",
			"核对交易所实时行情，确认是正常剧烈波动还是异常数据"
	case raw == "invalid_config":
		return "监控项配置不完整或阈值无效",
			"检查 Market Canary 的 Dataset、Symbol、Frequency 和阈值配置"
	case raw == "unreachable":
		return "SCF 无法连接目标服务",
			"检查目标服务进程、服务路由和网络连通性"
	case raw == "storage_error":
		return "Storage 拒绝了 SCF 的行情查询",
			"检查 Dataset 是否启用，以及 SCF 使用的 Storage 鉴权凭据"
	case raw == "decode":
		return "SCF 无法解析 Storage 返回的行情数据",
			"根据诊断编号查询 SCF 日志，并检查 K 线时间、收盘价和成交量字段"
	case raw == "http_status":
		if result.HTTPStatus > 0 {
			return fmt.Sprintf("目标服务返回异常状态（HTTP %d）", result.HTTPStatus),
				"检查目标服务健康状态和健康检查鉴权配置"
		}
		return "目标服务返回异常 HTTP 状态", "检查目标服务健康状态和健康检查鉴权配置"
	case raw == "invalid_target":
		return "SCF 健康检查地址无效", "检查 SCF 中配置的 Monitor 和 Gateway 健康检查地址"
	case raw == "auth_nonce" || raw == "auth_sign":
		return "SCF 生成健康检查签名失败", "检查 SCF 的 health-auth 配置，并根据诊断编号查询 SCF 日志"
	}
	if strings.HasPrefix(raw, "unexpected HTTP status ") {
		codeText := strings.TrimSpace(strings.TrimPrefix(raw, "unexpected HTTP status "))
		if code, err := strconv.Atoi(codeText); err == nil && code == 401 {
			target := firstText(check.Name, check.CheckID, "目标服务")
			return "目标服务拒绝了健康检查鉴权（HTTP 401）",
				fmt.Sprintf("检查 Monitor 与 %s 使用的 health-auth 密钥是否一致", targetName(target))
		}
		return "目标服务返回了非预期的 HTTP 状态码 " + codeText,
			"检查目标服务日志、健康检查地址和预期状态码配置"
	}
	if raw == "" {
		return firstText(result.Status, "未知异常"), "查看 Monitor 与目标服务日志获取详细原因"
	}
	return raw, "查看 Monitor 与目标服务日志获取详细原因"
}

func reporterCheckTarget(check domain.Check) (string, string) {
	parts := strings.Split(check.CheckID, ":")
	if len(parts) >= 3 && parts[0] == "reporter" {
		nodeID, serviceName := strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if serviceName != "" {
			if len(parts) >= 4 {
				if instanceID := strings.TrimSpace(parts[3]); instanceID != "" {
					return serviceName, instanceID
				}
			}
			if nodeID != "" {
				return serviceName, serviceName + "@" + nodeID
			}
			return serviceName, serviceName
		}
	}
	name := firstText(check.Name, check.CheckID, "目标服务")
	return targetName(name), name
}

func targetName(value string) string {
	if before, _, ok := strings.Cut(value, "@"); ok {
		return before
	}
	return value
}

func localizedEventType(eventType string) string {
	switch eventType {
	case domain.AlertEventTriggered:
		return "首次告警"
	case domain.AlertEventReminder:
		return "持续告警"
	case domain.AlertEventResolved:
		return "恢复通知"
	case domain.AlertEventSendFailed:
		return "通知失败"
	default:
		return firstText(eventType, "监控告警")
	}
}

func localizedAlertStatus(status string) string {
	switch status {
	case domain.AlertStatusFiring:
		return "异常中"
	case domain.AlertStatusResolved:
		return "已恢复"
	case domain.AlertStatusOK:
		return "正常"
	default:
		return firstText(status, "未知")
	}
}

func firstText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func newEventID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "event-" + hex.EncodeToString(b[:])
	}
	return "event-" + time.Now().UTC().Format("20060102150405.000000000")
}
