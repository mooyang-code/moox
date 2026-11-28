package command

import (
	"context"
	"fmt"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

const monitorService = "trpc.moox.monitor.MonitorMgr"

// 告警通知渠道不在配置文件里填写，安装完成后由用户在管理台「资源与运维 › 监控告警」录入；
// setup init 只读检查 Monitor 里的渠道数量，没有渠道时给出明确提醒。
const (
	notificationConfigured = "configured" // Monitor 已有至少一个告警渠道
	notificationEmpty      = "empty"      // Monitor 尚未配置任何告警渠道
)

// setupNotificationSummary 是 setup init 检查告警渠道数量的结果，不含任何 Webhook 地址或密钥。
type setupNotificationSummary struct {
	Status   string `json:"status"`
	Existing int    `json:"existing"`
	Warning  string `json:"warning,omitempty"`
}

// checkSetupNotificationChannels 只读查询 Monitor 里的告警渠道数；为空时返回提醒文案。
func checkSetupNotificationChannels(ctx context.Context, gateway gatewayclient.Invoker, monitorURL string) (*setupNotificationSummary, error) {
	listed := &monitorpb.ListNotificationChannelsRsp{}
	if err := invokeMonitor(ctx, gateway, "ListNotificationChannels", &monitorpb.ListNotificationChannelsReq{}, listed, listed.GetRetInfo); err != nil {
		return nil, err
	}
	existing := len(listed.GetChannels())
	if existing > 0 {
		return &setupNotificationSummary{Status: notificationConfigured, Existing: existing}, nil
	}
	return &setupNotificationSummary{
		Status:  notificationEmpty,
		Warning: fmt.Sprintf("尚未配置告警通知渠道，系统异常时不会推送通知；请打开管理台「资源与运维 › 监控告警」添加：%s", monitorURL),
	}, nil
}

// defaultSetupCheckNotification 检查 Monitor 是否已配置告警渠道。
func defaultSetupCheckNotification(ctx context.Context, snapshot *setupSnapshot) (*setupNotificationSummary, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("notification: setup configuration is missing")
	}
	gateway, err := openCommandGateway(ctx, "", snapshot.Manifest)
	if err != nil {
		return nil, fmt.Errorf("notification: open gateway: %w", err)
	}
	defer gateway.Close()
	monitorURL := ""
	if console := describeSetupConsoleLinks(snapshot); console != nil {
		monitorURL = console.MonitorURL
	}
	return checkSetupNotificationChannels(ctx, gateway, monitorURL)
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
