package policysync

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/mooyang-code/moox/packages/notification"
)

// 两条直推的判断（设计 3.8，D2）：Monitor 自己或控制机出问题时 Monitor 报不了，由非控制机的主机采集器按拉取主机策略的结果推送。
const (
	// UnreachableAfter 次连续拉取失败（每分钟一次）推送“控制机或 Monitor 不可达”。
	UnreachableAfter = 3
	// RestartWindow 内 Monitor 的启动时间变了 RestartChanges 次推送“Monitor 反复重启”：Monitor 反复崩溃时
	// 每次拉起后都能成功应答几次，光看连续失败发现不了。连续部署控制机也会触发这一条（误报，已写进文档）。
	RestartWindow  = 10 * time.Minute
	RestartChanges = 3
)

// Watch 按每次拉取主机策略的结果决定是否推送“控制机或 Monitor 不可达”“Monitor 反复重启”，两者恢复时各推一条恢复。
// 推送渠道取主机策略缓存里的渠道；本机部署了 Monitor（控制机）时不推送：控制机或 Monitor 坏了由其他主机报。
type Watch struct {
	host string
	// local 报告本机是否部署了 Monitor；为空表示不是。
	local func() bool
	// send 把一条消息发给缓存的主机策略里的全部渠道。
	send func(context.Context, notification.Message) error
	now  func() time.Time
	logf func(string, ...any)

	mu          sync.Mutex
	failures    int
	unreachable bool
	lastStart   string
	changes     []time.Time
	restarting  bool
}

// NewWatch 创建直推判断；policyPath 是主机策略缓存（推送渠道从这里读）。
func NewWatch(host, policyPath string, local func() bool, logf func(string, ...any)) *Watch {
	return &Watch{host: host, local: local, now: time.Now, logf: logf,
		send: func(ctx context.Context, message notification.Message) error { return sendToPolicyChannels(ctx, policyPath, message) }}
}

// Observe 记下一次拉取的结果：startedAt 是 Monitor 的进程启动时间，err 非空表示这次拉取失败。
func (w *Watch) Observe(ctx context.Context, startedAt string, err error) {
	if w == nil || (w.local != nil && w.local()) {
		return
	}
	now := w.now()
	var messages []notification.Message
	w.mu.Lock()
	if err != nil {
		w.failures++
		if w.failures >= UnreachableAfter && !w.unreachable {
			w.unreachable = true
			messages = append(messages, w.unreachableMessage(err))
		}
	} else {
		if w.unreachable {
			messages = append(messages, w.message("unreachable", notification.SeverityInfo, "控制机与 Monitor 已恢复可达",
				fmt.Sprintf("主机 %s 的采集器又能从 Monitor 拉到主机策略了（此前连续 %d 次失败）。", w.host, w.failures)))
		}
		w.failures, w.unreachable = 0, false
		if startedAt != "" && w.lastStart != "" && startedAt != w.lastStart {
			w.changes = append(w.changes, now)
		}
		if startedAt != "" {
			w.lastStart = startedAt
		}
	}
	kept := w.changes[:0]
	for _, at := range w.changes {
		if now.Sub(at) <= RestartWindow {
			kept = append(kept, at)
		}
	}
	w.changes = kept
	switch {
	case len(w.changes) >= RestartChanges && !w.restarting:
		w.restarting = true
		messages = append(messages, w.message("monitor_restart", notification.SeverityCritical, "Monitor 反复重启",
			fmt.Sprintf("主机 %s 的采集器看到 Monitor 的启动时间在 %d 分钟内变了 %d 次（最近一次启动 %s）：Monitor 可能在崩溃后被反复拉起，期间告警可能漏发。查看控制机上 monitor 的日志。10 分钟内连续部署控制机三次也会出现这一条。",
				w.host, int(RestartWindow/time.Minute), len(w.changes), w.lastStart)))
	case len(w.changes) < RestartChanges && w.restarting:
		w.restarting = false
		messages = append(messages, w.message("monitor_restart", notification.SeverityInfo, "Monitor 不再反复重启",
			fmt.Sprintf("最近 %d 分钟里 Monitor 的启动时间变化少于 %d 次。", int(RestartWindow/time.Minute), RestartChanges)))
	}
	w.mu.Unlock()
	for _, message := range messages {
		if err := w.send(ctx, message); err != nil && w.logf != nil {
			w.logf("host-agent direct alert %q not delivered: %v", message.Title, err)
		}
	}
}

func (w *Watch) unreachableMessage(err error) notification.Message {
	return w.message("unreachable", notification.SeverityCritical, "控制机或 Monitor 不可达",
		fmt.Sprintf("主机 %s 的采集器连续 %d 次（每分钟一次）没能从 Monitor 拉到主机策略：控制机、它的主机网关或 Monitor 可能宕机，期间 Monitor 的告警都发不出来。最近一次错误：%s",
			w.host, w.failures, firstLine(err.Error())))
}

func (w *Watch) message(kind string, severity notification.Severity, title, body string) notification.Message {
	return notification.Message{
		Key: "host-agent:" + w.host + ":" + kind, Severity: severity,
		Title: "【MooX】" + title + "（由 " + w.host + " 观测）", Body: body,
		Labels: map[string]string{"host": w.host, "kind": kind},
	}
}

// sendToPolicyChannels 发给主机策略缓存里的全部渠道；至少一个渠道送达就算送达。
func sendToPolicyChannels(ctx context.Context, path string, message notification.Message) error {
	policy, err := hostpolicy.Read(path)
	if err != nil {
		return fmt.Errorf("读取主机策略缓存失败：%w", err)
	}
	if len(policy.NotificationChannels) == 0 {
		return fmt.Errorf("主机策略缓存里没有告警渠道")
	}
	var failures []string
	for _, channel := range policy.NotificationChannels {
		sender, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(channel.Type), WebhookURL: channel.WebhookURL, Secret: channel.Secret})
		if err == nil {
			sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = sender.Send(sendCtx, message)
			cancel()
		}
		if err == nil {
			return nil
		}
		failures = append(failures, channel.Type+"："+strings.ReplaceAll(err.Error(), channel.WebhookURL, "<webhook>"))
	}
	return fmt.Errorf("全部渠道推送失败：%s", strings.Join(failures, "；"))
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
