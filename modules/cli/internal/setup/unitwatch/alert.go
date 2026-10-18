package unitwatch

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/mooyang-code/moox/packages/notification"
)

// 告警种类，用作限频与合并的键。
const (
	alertRestart   = "restart"
	alertCrashLoop = "crash_loop"
	alertRecovered = "recovered"
	alertFailed    = "start_failed"
	alertIdentity  = "identity"
	alertNotReady  = "not_ready"
	alertDraining  = "draining"
	alertBarrier   = "barrier"
)

// manualAlertInterval 是需要人工处理的问题（拉起失败、身份不符、持续未就绪、停止被打断、持久屏障）同一种最多多久提醒一次。
const manualAlertInterval = 30 * time.Minute

// beijing 是告警正文里的时区（与 Monitor 的告警文案一致）。
var beijing = time.FixedZone("CST", 8*3600)

// alertState 是跨轮次的告警状态：每个组件每种告警最后一次发送的时间，以及合并窗口内还没发出的拉起次数。
type alertState struct {
	LastSent        map[string]time.Time `json:"last_sent,omitempty"`
	PendingRestarts map[string]int       `json:"pending_restarts,omitempty"`
}

func (a *alertState) due(key string, now time.Time, interval time.Duration) bool {
	last, ok := a.LastSent[key]
	return !ok || now.Sub(last) >= interval
}

func (a *alertState) mark(key string, now time.Time) {
	if a.LastSent == nil {
		a.LastSent = map[string]time.Time{}
	}
	a.LastSent[key] = now
}

// loadPolicy 读取主机策略缓存；不存在时返回空策略（没有渠道，守护参数用默认值）。
func loadPolicy(path string) (hostpolicy.Policy, bool, error) {
	policy, err := hostpolicy.Read(path)
	if os.IsNotExist(err) {
		return hostpolicy.Policy{}, false, nil
	}
	if err != nil {
		return hostpolicy.Policy{}, false, err
	}
	return policy, true, nil
}

// applyWatchdog 把策略缓存里的守护参数覆盖到默认值上（空值保持默认）。
func applyWatchdog(base Policy, w hostpolicy.Watchdog) Policy {
	set := func(target *time.Duration, value string) {
		if parsed, err := hostpolicy.Duration(value); err == nil && parsed > 0 {
			*target = parsed
		}
	}
	if w.CrashLoopFailures > 0 {
		base.CrashLoopFailures = w.CrashLoopFailures
	}
	set(&base.CrashLoopWindow, w.CrashLoopWindow)
	set(&base.CrashLoopRetry, w.CrashLoopRetry)
	set(&base.CrashLoopSlow, w.CrashLoopSlow)
	set(&base.StableAfter, w.StableAfter)
	set(&base.MergeWindow, w.MergeWindow)
	set(&base.NotReadyAlert, w.NotReadyAlert)
	return base
}

// planAlerts 根据本轮报告决定要发出的告警，并更新告警状态。
func planAlerts(report Report, state *watchState, now time.Time, p Policy) []notification.Message {
	alerts := &state.Alerts
	var messages []notification.Message
	host := report.HostID
	key := func(component, kind string) string { return component + "/" + kind }
	message := func(component, kind string, severity notification.Severity, title string, lines ...string) {
		lines = append(lines, "时间："+now.In(beijing).Format("2006-01-02 15:04:05")+"（北京时间）")
		messages = append(messages, notification.Message{
			Key:      fmt.Sprintf("watchdog:%s:%s:%s", host, component, kind),
			Severity: severity,
			Title:    title,
			Body:     strings.Join(lines, "\n"),
			Labels:   map[string]string{"host": host, "component": component, "kind": kind},
		})
		alerts.mark(key(component, kind), now)
	}

	if report.Barrier != "" {
		if alerts.due(key("*", alertBarrier), now, manualAlertInterval) {
			command := "moox-runtime recover --unit-root <单元根>"
			if report.Barrier == "bootstrap" {
				command = "按初始化（bootstrap）的恢复流程重新执行"
			}
			message("*", alertBarrier, notification.SeverityWarning, "【MooX 守护】"+host+" · 需要人工恢复",
				"原因："+map[string]string{"installation": "发布被中断（run/installation.json）", "bootstrap": "初始化被中断（run/bootstrap.json）"}[report.Barrier],
				"影响：守护暂停自动拉起，直到恢复完成",
				"处理："+command)
		}
		return messages
	}

	rebootRound := len(report.BootRecovered) > 0
	var rebootStarted, rebootFailed []string
	for _, c := range report.Components {
		component := c.Component
		switch {
		case c.EnteredCrashLoop:
			message(component, alertCrashLoop, notification.SeverityCritical, "【MooX 自动拉起】"+host+" · "+component+" · 持续崩溃",
				withEvidence(c, fmt.Sprintf("原因：%s 内自动拉起 %d 次", durationText(p.CrashLoopWindow), c.RecentRestarts),
					fmt.Sprintf("处理：已降低拉起频率（每 %s 尝试一次），请检查组件日志 run/logs/%s.log", durationText(p.CrashLoopRetry), component))...)
			delete(alerts.PendingRestarts, component)
		case c.Recovered:
			message(component, alertRecovered, notification.SeverityInfo, "【MooX 自动拉起】"+host+" · "+component+" · 持续崩溃已恢复",
				fmt.Sprintf("当前：已连续正常运行 %s", durationText(p.StableAfter)))
		case (c.Outcome == OutcomeStarted || c.Outcome == OutcomeRestarted) && rebootRound:
			rebootStarted = append(rebootStarted, component)
		case c.Outcome == OutcomeStartFailed && rebootRound:
			rebootFailed = append(rebootFailed, component)
		case c.Outcome == OutcomeStarted || c.Outcome == OutcomeRestarted:
			if !alerts.due(key(component, alertRestart), now, p.MergeWindow) {
				if alerts.PendingRestarts == nil {
					alerts.PendingRestarts = map[string]int{}
				}
				alerts.PendingRestarts[component]++
				continue
			}
			reason := "进程不存在"
			if c.Outcome == OutcomeRestarted {
				reason = "存活检查失败，已停止并重启"
			}
			lines := []string{"原因：" + reason, fmt.Sprintf("结果：拉起成功，健康检查通过（耗时 %.1f 秒）", float64(c.DurationMS)/1000)}
			if pending := alerts.PendingRestarts[component]; pending > 0 {
				lines = append(lines, fmt.Sprintf("另外：上次通知后还自动拉起过 %d 次", pending))
			}
			lines = append(lines, fmt.Sprintf("近 1 小时自动拉起：%d 次", c.RecentRestarts))
			message(component, alertRestart, notification.SeverityWarning, "【MooX 自动拉起】"+host+" · "+component+" · 已恢复", withEvidence(c, lines...)...)
			delete(alerts.PendingRestarts, component)
		case c.Outcome == OutcomeStartFailed:
			if alerts.due(key(component, alertFailed), now, manualAlertInterval) {
				message(component, alertFailed, notification.SeverityCritical, "【MooX 自动拉起】"+host+" · "+component+" · 拉起失败",
					withEvidence(c, "原因："+firstLine(c.Error), fmt.Sprintf("处理：守护会继续重试；请检查 run/logs/%s.log", component))...)
			}
		case c.Outcome == OutcomeIdentityError:
			if alerts.due(key(component, alertIdentity), now, manualAlertInterval) {
				message(component, alertIdentity, notification.SeverityCritical, "【MooX 守护】"+host+" · "+component+" · 需要人工处理",
					"原因：进程身份与记录不符，运行助手拒绝操作该进程",
					"处理：确认对应进程后，用 moox-runtime stop --components "+component+" 处理")
			}
		case c.Outcome == OutcomeNotReady && c.NotReadySeconds >= int64(p.NotReadyAlert/time.Second):
			if alerts.due(key(component, alertNotReady), now, manualAlertInterval) {
				message(component, alertNotReady, notification.SeverityWarning, "【MooX 守护】"+host+" · "+component+" · 持续未就绪",
					fmt.Sprintf("原因：进程存活，但 /readyz 已连续 %s 未就绪（不自动重启）", durationText(time.Duration(c.NotReadySeconds)*time.Second)),
					"处理：查看组件的 readyz 详情与日志")
			}
		case c.Outcome == OutcomeSkippedDraining:
			if alerts.due(key(component, alertDraining), now, manualAlertInterval) {
				message(component, alertDraining, notification.SeverityWarning, "【MooX 守护】"+host+" · "+component+" · 需要人工处理",
					"原因：上一次停止过程被中断（run/draining/"+component+"），守护不会自动拉起",
					"处理：moox-runtime stop --components "+component+" 接续停止后再启动")
			}
		}
	}
	if len(rebootStarted) > 0 || len(rebootFailed) > 0 {
		sort.Strings(rebootStarted)
		sort.Strings(rebootFailed)
		severity := notification.SeverityWarning
		lines := []string{fmt.Sprintf("已拉起 %d 个组件：%s", len(rebootStarted), strings.Join(rebootStarted, "、"))}
		if len(rebootFailed) > 0 {
			severity = notification.SeverityCritical
			lines = append(lines, fmt.Sprintf("拉起失败 %d 个：%s（守护会继续重试）", len(rebootFailed), strings.Join(rebootFailed, "、")))
		}
		if bootedAt, err := systemBootTime(); err == nil {
			lines = append(lines, "开机时间："+bootedAt.In(beijing).Format("2006-01-02 15:04:05"))
		}
		message("*", "reboot", severity, "【MooX 自动拉起】"+host+" · 主机重启", lines...)
	}
	return messages
}

func withEvidence(c ComponentReport, lines ...string) []string {
	if len(c.Evidence) > 0 {
		lines = append(lines, "崩溃线索："+strings.Join(c.Evidence, " | "))
	}
	return lines
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if runes := []rune(text); len(runes) > 200 {
		text = string(runes[:200]) + "…"
	}
	return text
}

func durationText(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d 小时", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	default:
		return fmt.Sprintf("%d 秒", int(d/time.Second))
	}
}

// sendAlerts 发给全部渠道；至少一个渠道送达就算送达。推送失败只记进报告，不影响守护。
func sendAlerts(ctx context.Context, messages []notification.Message, policy hostpolicy.Policy, newSender func(notification.ChannelConfig) (notification.Sender, error)) []string {
	if len(messages) == 0 {
		return nil
	}
	if len(policy.NotificationChannels) == 0 {
		return []string{"没有可用的告警渠道（主机策略缓存缺失或渠道为空），本轮告警未发送"}
	}
	var errs []string
	for _, msg := range messages {
		delivered := false
		var failures []string
		for _, channel := range policy.NotificationChannels {
			sender, err := newSender(notification.ChannelConfig{Type: notification.ChannelType(channel.Type), WebhookURL: channel.WebhookURL})
			if err == nil {
				sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err = sender.Send(sendCtx, msg)
				cancel()
			}
			if err != nil {
				failures = append(failures, channel.Type+"："+redact(err.Error(), channel.WebhookURL))
				continue
			}
			delivered = true
		}
		if !delivered {
			errs = append(errs, msg.Title+" 推送失败："+strings.Join(failures, "；"))
		}
	}
	return errs
}

// redact 防止错误信息里带出完整的 webhook 地址（地址本身就是推送凭据）。
func redact(text, webhook string) string {
	if webhook == "" {
		return text
	}
	return strings.ReplaceAll(text, webhook, "<webhook>")
}
