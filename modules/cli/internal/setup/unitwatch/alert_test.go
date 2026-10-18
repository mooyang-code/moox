package unitwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/require"
)

const testWebhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret-key"

type captured struct {
	messages []notification.Message
	fail     error
}

func (c *captured) Send(_ context.Context, m notification.Message) error {
	if c.fail != nil {
		return c.fail
	}
	c.messages = append(c.messages, m)
	return nil
}

// alertHost 是带主机策略缓存与假发送器的 fakeHost。
func alertHost(t *testing.T, units map[string][]string, watchdog hostpolicy.Watchdog) (*fakeHost, *captured, func() Report) {
	t.Helper()
	h := newFakeHost(t, units)
	require.NoError(t, hostpolicy.Write(hostpolicy.Path(h.root), hostpolicy.Policy{
		Version: 1, Source: hostpolicy.SourceMonitor, FetchedAt: h.now,
		NotificationChannels: []hostpolicy.Channel{{Type: "wecom", WebhookURL: testWebhook}},
		Watchdog:             watchdog,
	}))
	sink := &captured{}
	run := func() Report {
		t.Helper()
		h.calls, h.operations = nil, nil
		opts := h.options()
		opts.NewSender = func(cfg notification.ChannelConfig) (notification.Sender, error) {
			require.Equal(t, testWebhook, cfg.WebhookURL)
			return sink, nil
		}
		report, err := Run(context.Background(), opts)
		require.NoError(t, err)
		return report
	}
	return h, sink, run
}

func started() func(string) (unitruntime.Result, error) {
	return func(string) (unitruntime.Result, error) {
		return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 5, Ready: true, Action: "started"}}}, nil
	}
}

func titles(messages []notification.Message) []string {
	var out []string
	for _, m := range messages {
		out = append(out, m.Title)
	}
	return out
}

// 同一组件 10 分钟内的拉起只发一条，下一条写明期间还拉起过几次；崩溃线索与近 1 小时次数写进正文。
func TestAlertsMergeRestartsWithinWindow(t *testing.T) {
	h, sink, run := alertHost(t, map[string][]string{"prod": {"strategy"}}, hostpolicy.Watchdog{CrashLoopFailures: 10})
	require.NoError(t, os.WriteFile(filepath.Join(h.root, "run", "logs", "strategy.log"), []byte("panic: boom\n"), 0o600))
	h.respond = started()

	report := run()
	require.Len(t, sink.messages, 1)
	first := sink.messages[0]
	require.Equal(t, "【MooX 自动拉起】control · strategy · 已恢复", first.Title)
	require.Equal(t, notification.SeverityWarning, first.Severity)
	require.Contains(t, first.Body, "原因：进程不存在")
	require.Contains(t, first.Body, "崩溃线索：panic: boom")
	require.Contains(t, first.Body, "近 1 小时自动拉起：1 次")
	require.Contains(t, first.Body, "（北京时间）")
	require.Equal(t, []string{first.Title}, report.Alerts)

	h.now = h.now.Add(time.Minute)
	run()
	require.Len(t, sink.messages, 1, "合并窗口内不再单独发")

	h.now = h.now.Add(10 * time.Minute)
	run()
	require.Len(t, sink.messages, 2)
	require.Contains(t, sink.messages[1].Body, "另外：上次通知后还自动拉起过 1 次")
	require.Contains(t, sink.messages[1].Body, "近 1 小时自动拉起：3 次")
}

// 进入持续崩溃立即发严重告警（不受合并窗口影响）。
func TestAlertsCrashLoopIsCritical(t *testing.T) {
	h, sink, run := alertHost(t, map[string][]string{"prod": {"collector"}}, hostpolicy.Watchdog{})
	h.respond = started()
	for i := 0; i < 3; i++ {
		run()
		h.now = h.now.Add(time.Minute)
	}
	require.Equal(t, []string{
		"【MooX 自动拉起】control · collector · 已恢复",
		"【MooX 自动拉起】control · collector · 持续崩溃",
	}, titles(sink.messages))
	require.Equal(t, notification.SeverityCritical, sink.messages[1].Severity)
	require.Contains(t, sink.messages[1].Body, "10 分钟 内自动拉起 3 次")
}

// 主机重启后的首轮：多个组件的拉起合并成一条“主机重启”，不再逐个发。
func TestAlertsRebootRoundIsMerged(t *testing.T) {
	previous := systemBootTime
	t.Cleanup(func() { systemBootTime = previous })
	systemBootTime = func() (time.Time, error) { return time.Date(2026, 10, 18, 2, 58, 39, 0, time.UTC), nil }
	h, sink, run := alertHost(t, map[string][]string{"prod": {"admin", "monitor", "strategy"}}, hostpolicy.Watchdog{})
	h.respond = func(component string) (unitruntime.Result, error) {
		result := unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 5, Ready: true, Action: "started"}}}
		if component == "admin" {
			result.BootRecovered = []unitruntime.BootRecovery{{Component: "monitor", Kind: "pid"}}
		}
		if component == "strategy" {
			return unitruntime.Result{Components: []unitruntime.Status{{State: "stopped", Action: "started"}}}, errors.New("release process did not become ready")
		}
		return result, nil
	}
	run()
	require.Len(t, sink.messages, 1)
	require.Equal(t, "【MooX 自动拉起】control · 主机重启", sink.messages[0].Title)
	require.Equal(t, notification.SeverityCritical, sink.messages[0].Severity, "有组件拉起失败时升为严重")
	require.Contains(t, sink.messages[0].Body, "已拉起 2 个组件：admin、monitor")
	require.Contains(t, sink.messages[0].Body, "拉起失败 1 个：strategy")
	require.Contains(t, sink.messages[0].Body, "开机时间：2026-10-18 10:58:39")
}

// 持久屏障：整轮跳过并提醒人工恢复，30 分钟内不重复。
func TestAlertsBarrierIsRateLimited(t *testing.T) {
	h, sink, run := alertHost(t, map[string][]string{"prod": {"admin"}}, hostpolicy.Watchdog{})
	require.NoError(t, os.WriteFile(filepath.Join(h.root, "run", "bootstrap.json"), []byte("{}"), 0o600))
	run()
	h.now = h.now.Add(10 * time.Minute)
	run()
	require.Equal(t, []string{"【MooX 守护】control · 需要人工恢复"}, titles(sink.messages))
	require.Contains(t, sink.messages[0].Body, "初始化被中断")
	h.now = h.now.Add(25 * time.Minute)
	run()
	require.Len(t, sink.messages, 2)
}

// 持续未就绪超过 5 分钟才告警；身份不符与停止被打断提醒人工处理。
func TestAlertsManualConditions(t *testing.T) {
	h, sink, run := alertHost(t, map[string][]string{"prod": {"monitor", "cloudnode", "factor-mgr"}}, hostpolicy.Watchdog{})
	h.respond = func(component string) (unitruntime.Result, error) {
		switch component {
		case "monitor":
			return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 3}}}, nil
		case "cloudnode":
			return unitruntime.Result{Components: []unitruntime.Status{{State: "draining", PID: 4}}}, nil
		}
		return unitruntime.Result{}, errIdentityForTest
	}
	for i := 0; i < 6; i++ {
		run()
		h.now = h.now.Add(time.Minute)
	}
	got := titles(sink.messages)
	require.Contains(t, got, "【MooX 守护】control · cloudnode · 需要人工处理")
	require.Contains(t, got, "【MooX 守护】control · monitor · 持续未就绪")
	require.Len(t, got, 2, "身份错误在这里用的是普通错误，归为检查失败，不提醒；每种问题只发一次")
}

var errIdentityForTest = errors.New("ordinary failure")

// 没有渠道时记录原因；推送失败时错误信息里不带出 webhook。
func TestAlertsWithoutChannelsOrFailingSender(t *testing.T) {
	h := newFakeHost(t, map[string][]string{"prod": {"strategy"}})
	h.respond = started()
	report := h.run()
	require.Contains(t, strings.Join(report.AlertErrors, "\n"), "没有可用的告警渠道")

	h2, sink, run := alertHost(t, map[string][]string{"prod": {"strategy"}}, hostpolicy.Watchdog{})
	h2.respond = started()
	sink.fail = errors.New("post " + testWebhook + ": connection refused")
	failed := run()
	joined := strings.Join(failed.AlertErrors, "\n")
	require.Contains(t, joined, "推送失败")
	require.NotContains(t, joined, "secret-key")
}

// 中心策略关闭守护：只检查（status），不拉起、不告警。
func TestAlertsDisabledWatchdogOnlyInspects(t *testing.T) {
	h, sink, run := alertHost(t, map[string][]string{"prod": {"strategy"}}, hostpolicy.Watchdog{Disabled: true})
	h.respond = func(string) (unitruntime.Result, error) {
		return unitruntime.Result{Components: []unitruntime.Status{{State: "stopped"}}}, nil
	}
	report := run()
	require.True(t, report.WatchdogDisabled)
	require.Equal(t, []string{"status"}, h.operations)
	require.Equal(t, OutcomeWouldStart, report.Components[0].Outcome)
	require.Empty(t, sink.messages)
}
