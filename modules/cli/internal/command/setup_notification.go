package command

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

const monitorService = "trpc.moox.monitor.MonitorMgr"

// 告警渠道的初始值（秘密文件的 [notification]）只在 Monitor 还没有任何渠道时由 setup init 写入一次；
// 之后以管理台「监控 › 告警渠道」为准，这里不比对、不改、不删。
const (
	notificationCreated = "created" // 本次写入
	notificationKept    = "kept"    // Monitor 已有渠道，以管理台为准，没有写入
	notificationSkipped = "skipped" // 秘密文件里没有填 Webhook 地址
)

// setupNotificationSummary 是 setup init 写告警渠道的结果；地址只给打码后的形式，不含密钥。
type setupNotificationSummary struct {
	Status   string `json:"status"`
	Type     string `json:"type,omitempty"`
	Webhook  string `json:"webhook,omitempty"`
	Existing int    `json:"existing,omitempty"`
	Note     string `json:"note,omitempty"`
}

// ensureInitialNotification 列出 Monitor 的告警渠道：一个都没有才写入 initial，已有任何渠道（不管地址是不是这个）都不动。
func ensureInitialNotification(ctx context.Context, gateway gatewayclient.Invoker, initial topology.Notification) (*setupNotificationSummary, error) {
	initial.WebhookURL = strings.TrimSpace(initial.WebhookURL)
	if initial.WebhookURL == "" {
		return &setupNotificationSummary{Status: notificationSkipped, Note: "秘密文件里没有填告警渠道的 Webhook 地址；部署后在管理台「监控 › 告警渠道」里添加"}, nil
	}
	channelType := strings.TrimSpace(initial.ChannelType)
	if channelType == "" {
		channelType = "wecom"
	}
	listed := &monitorpb.ListNotificationChannelsRsp{}
	if err := invokeMonitor(ctx, gateway, "ListNotificationChannels", &monitorpb.ListNotificationChannelsReq{}, listed, listed.GetRetInfo); err != nil {
		return nil, err
	}
	if existing := len(listed.GetChannels()); existing > 0 {
		return &setupNotificationSummary{Status: notificationKept, Existing: existing, Note: "Monitor 已有告警渠道，以管理台为准，没有写入"}, nil
	}
	added := &monitorpb.AddNotificationChannelRsp{}
	request := &monitorpb.AddNotificationChannelReq{ChannelType: channelType, WebhookUrl: initial.WebhookURL, Secret: strings.TrimSpace(initial.Secret)}
	if err := invokeMonitor(ctx, gateway, "AddNotificationChannel", request, added, added.GetRetInfo); err != nil {
		return nil, err
	}
	return &setupNotificationSummary{Status: notificationCreated, Type: channelType, Webhook: maskWebhook(initial.WebhookURL)}, nil
}

// defaultSetupEnsureNotification 是 setup init 的告警渠道一步：没有填地址时不连网关。
func defaultSetupEnsureNotification(ctx context.Context, snapshot *setupconfig.Snapshot) (*setupNotificationSummary, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("notification: setup configuration is missing")
	}
	initial := snapshot.Manifest.Notification
	if strings.TrimSpace(initial.WebhookURL) == "" {
		return ensureInitialNotification(ctx, nil, initial)
	}
	gateway, err := openCommandGateway(ctx, "", snapshot.Config())
	if err != nil {
		return nil, fmt.Errorf("notification: open gateway: %w", err)
	}
	defer gateway.Close()
	return ensureInitialNotification(ctx, gateway, initial)
}

// invokeMonitor 调用一个 Monitor 方法并检查业务返回码；错误信息不含请求内容（可能有 Webhook）。
func invokeMonitor(ctx context.Context, gateway gatewayclient.Invoker, method string, req, rsp any, ret func() *commonpb.RetInfo) error {
	if err := gateway.Invoke(ctx, monitorService, method, req, rsp); err != nil {
		return fmt.Errorf("monitor %s: %w", method, err)
	}
	if info := ret(); info.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("monitor %s: %s %s", method, info.GetCode(), info.GetMsg())
	}
	return nil
}

// maskWebhook 只保留平台域名与地址末 4 位，用于在终端与日志中区分渠道。
func maskWebhook(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "***"
	}
	tail := raw
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return parsed.Scheme + "://" + parsed.Host + "/…" + tail
}
