package command

import (
	"bytes"
	"context"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const (
	tomlWebhook    = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=toml-1234"
	consoleWebhook = "https://open.feishu.cn/open-apis/bot/v2/hook/console-5678"
)

// fakeMonitor 模拟 Monitor 的通知渠道方法。
type fakeMonitor struct {
	channels []*monitorpb.NotificationChannel
	calls    []string
}

func (f *fakeMonitor) Invoke(_ context.Context, service, method string, req, rsp any) error {
	f.calls = append(f.calls, method)
	ok := &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
	switch method {
	case "ListNotificationChannels":
		proto.Merge(rsp.(*monitorpb.ListNotificationChannelsRsp), &monitorpb.ListNotificationChannelsRsp{RetInfo: ok, Channels: f.channels})
	case "AddNotificationChannel":
		in := req.(*monitorpb.AddNotificationChannelReq)
		f.channels = append(f.channels, &monitorpb.NotificationChannel{ChannelId: "new", ChannelType: in.GetChannelType(), WebhookUrl: in.GetWebhookUrl()})
		rsp.(*monitorpb.AddNotificationChannelRsp).RetInfo = ok
	case "DeleteNotificationChannel":
		rsp.(*monitorpb.DeleteNotificationChannelRsp).RetInfo = ok
	}
	return nil
}
func (f *fakeMonitor) Close() error { return nil }

func runNotificationCommand(t *testing.T, monitor *fakeMonitor, args ...string) (string, error) {
	t.Helper()
	snapshot := setupSnapshot(t)
	snapshot.Manifest.Notification = setupconfig.Notification{ChannelType: "wecom", WebhookURL: tomlWebhook}
	cmd := newConfigNotificationCommand(notificationDeps{
		load:    func(string) (*setupconfig.Snapshot, error) { return snapshot, nil },
		gateway: func(context.Context, *setupconfig.Snapshot) (notificationGateway, error) { return monitor, nil },
		hostStatus: func(_ context.Context, _ *setupconfig.Snapshot, host setupconfig.Host) (hostpolicy.Summary, error) {
			return hostpolicy.Summary{Path: "/data/moox/run/host-policy.json", Present: true, Version: 7, Source: hostpolicy.SourceMonitor, Channels: 1}, nil
		},
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs(append([]string{"--file", "moox.toml"}, args...))
	err := cmd.Execute()
	return output.String(), err
}

// 计划只读；Monitor 缺少 moox.toml 的渠道时计划添加；控制台添加的渠道默认保留，--prune 才删除；输出不含完整 Webhook。
func TestConfigNotificationPlansAndPublishes(t *testing.T) {
	monitor := &fakeMonitor{channels: []*monitorpb.NotificationChannel{{ChannelId: "c1", ChannelType: "feishu", WebhookUrl: consoleWebhook}}}
	output, err := runNotificationCommand(t, monitor)
	require.NoError(t, err)
	require.Equal(t, []string{"ListNotificationChannels"}, monitor.calls)
	require.Contains(t, output, `"action":"add"`)
	require.NotContains(t, output, `"action":"delete"`)
	require.Contains(t, output, `"version":7`)
	require.NotContains(t, output, tomlWebhook)
	require.NotContains(t, output, consoleWebhook)
	require.Contains(t, output, "…1234")

	_, err = runNotificationCommand(t, monitor, "--publish")
	require.ErrorContains(t, err, "--yes")

	monitor.calls = nil
	_, err = runNotificationCommand(t, monitor, "--publish", "--yes", "--prune")
	require.NoError(t, err)
	require.Equal(t, []string{"ListNotificationChannels", "AddNotificationChannel", "DeleteNotificationChannel"}, monitor.calls, "先添加再删除")

	monitor.channels = []*monitorpb.NotificationChannel{{ChannelId: "c2", ChannelType: "wecom", WebhookUrl: tomlWebhook}}
	output, err = runNotificationCommand(t, monitor)
	require.NoError(t, err)
	require.Contains(t, output, `"changed":0`)
	require.Contains(t, output, `"in_moox_toml":true`)
}
