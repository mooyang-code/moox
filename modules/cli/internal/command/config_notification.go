package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/spf13/cobra"
)

const monitorService = "trpc.moox.monitor.MonitorMgr"

// notificationGateway 是调用 Monitor 通知渠道方法所需的网关客户端。
type notificationGateway interface {
	gatewayclient.Invoker
	io.Closer
}

type notificationDeps struct {
	load       func(string) (*setupconfig.Snapshot, error)
	gateway    func(context.Context, *setupconfig.Snapshot) (notificationGateway, error)
	hostStatus func(context.Context, *setupconfig.Snapshot, setupconfig.Host) (hostpolicy.Summary, error)
}

func defaultNotificationDeps() notificationDeps {
	return notificationDeps{
		load: defaultSetupDeps().load,
		gateway: func(ctx context.Context, snapshot *setupconfig.Snapshot) (notificationGateway, error) {
			return openCommandGateway(ctx, "", snapshot)
		},
		hostStatus: defaultHostPolicyStatus,
	}
}

// notificationAction 是一次渠道变更；Webhook 只输出脱敏后的形式。
type notificationAction struct {
	Action    string `json:"action"`
	ChannelID string `json:"channel_id,omitempty"`
	Type      string `json:"type"`
	Webhook   string `json:"webhook"`

	url string
}

type notificationChannelView struct {
	ChannelID string `json:"channel_id"`
	Type      string `json:"type"`
	Webhook   string `json:"webhook"`
	InToml    bool   `json:"in_moox_toml"`
}

// newConfigNotificationCommand 对比 moox.toml 的 [notification] 与 Monitor 的通知渠道（告警渠道的唯一来源），
// 确认后写入 Monitor；同时列出各主机策略缓存的版本与拉取时间，HostAgent 每 5 分钟同步一次。
func newConfigNotificationCommand(deps notificationDeps) *cobra.Command {
	var file string
	var publish, yes, prune, noHosts bool
	cmd := &cobra.Command{
		Use:   "notification",
		Short: "对比 moox.toml 与 Monitor 的告警渠道，确认后写入 Monitor，并显示各主机缓存版本",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if publish && !yes {
				return errors.New("config notification --publish changes Monitor notification channels; review the plan first and pass --yes")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := snapshot.Manifest.RequireNotification(); err != nil {
				return err
			}
			gateway, err := deps.gateway(cmd.Context(), snapshot)
			if err != nil {
				return err
			}
			defer gateway.Close()
			listed := &monitorpb.ListNotificationChannelsRsp{}
			if err := invokeMonitor(cmd.Context(), gateway, "ListNotificationChannels", &monitorpb.ListNotificationChannelsReq{}, listed, listed.GetRetInfo); err != nil {
				return err
			}
			channels, actions := planNotification(snapshot.Manifest.Notification, listed.GetChannels(), prune)
			output := map[string]any{"monitor": channels, "actions": actions, "changed": len(actions)}
			if publish {
				applied, err := applyNotification(cmd.Context(), gateway, actions)
				output["applied"] = applied
				if err != nil {
					output["error"] = err.Error()
					_ = writeSetupJSON(cmd, output)
					return err
				}
			}
			hosts := map[string]any{}
			for _, host := range snapshot.Manifest.Hosts() {
				if noHosts {
					break
				}
				summary, err := deps.hostStatus(cmd.Context(), snapshot, host)
				if err != nil {
					hosts[host.Name] = map[string]string{"error": err.Error()}
					continue
				}
				hosts[host.Name] = summary
			}
			if !noHosts {
				output["hosts"] = hosts
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, output)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 路径")
	cmd.Flags().BoolVar(&publish, "publish", false, "把差异写入 Monitor（需同时传 --yes）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认写入")
	cmd.Flags().BoolVar(&prune, "prune", false, "同时删除 moox.toml 中没有的渠道（默认保留在控制台添加的渠道）")
	cmd.Flags().BoolVar(&noHosts, "no-hosts", false, "不经 SSH 查询各主机的策略缓存（只对比 Monitor 渠道）")
	return cmd
}

// planNotification 计算让 Monitor 包含 moox.toml 渠道所需的变更；--prune 时再删除其余渠道。
func planNotification(desired setupconfig.Notification, existing []*monitorpb.NotificationChannel, prune bool) ([]notificationChannelView, []notificationAction) {
	typ := strings.TrimSpace(desired.ChannelType)
	if typ == "" {
		typ = "wecom"
	}
	want := strings.TrimSpace(desired.WebhookURL)
	views := make([]notificationChannelView, 0, len(existing))
	var actions []notificationAction
	found := false
	for _, channel := range existing {
		match := channel.GetChannelType() == typ && channel.GetWebhookUrl() == want
		found = found || match
		views = append(views, notificationChannelView{ChannelID: channel.GetChannelId(), Type: channel.GetChannelType(), Webhook: maskWebhook(channel.GetWebhookUrl()), InToml: match})
		if prune && !match {
			actions = append(actions, notificationAction{Action: "delete", ChannelID: channel.GetChannelId(), Type: channel.GetChannelType(), Webhook: maskWebhook(channel.GetWebhookUrl())})
		}
	}
	if !found {
		// 先添加再删除，过程中始终至少有一个渠道。
		actions = append([]notificationAction{{Action: "add", Type: typ, Webhook: maskWebhook(want), url: want}}, actions...)
	}
	return views, actions
}

func applyNotification(ctx context.Context, gateway gatewayclient.Invoker, actions []notificationAction) ([]string, error) {
	var applied []string
	for _, action := range actions {
		switch action.Action {
		case "add":
			rsp := &monitorpb.AddNotificationChannelRsp{}
			if err := invokeMonitor(ctx, gateway, "AddNotificationChannel", &monitorpb.AddNotificationChannelReq{ChannelType: action.Type, WebhookUrl: action.url}, rsp, rsp.GetRetInfo); err != nil {
				return applied, err
			}
			applied = append(applied, "add "+action.Type+" "+action.Webhook)
		case "delete":
			rsp := &monitorpb.DeleteNotificationChannelRsp{}
			if err := invokeMonitor(ctx, gateway, "DeleteNotificationChannel", &monitorpb.DeleteNotificationChannelReq{ChannelId: action.ChannelID}, rsp, rsp.GetRetInfo); err != nil {
				return applied, err
			}
			applied = append(applied, "delete "+action.ChannelID)
		}
	}
	return applied, nil
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

// maskWebhook 只保留平台域名与密钥末 4 位，用于在终端与日志中区分渠道。
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

// defaultHostPolicyStatus 经 SSH 读取主机策略缓存的概要（不含推送地址）。
func defaultHostPolicyStatus(ctx context.Context, snapshot *setupconfig.Snapshot, host setupconfig.Host) (hostpolicy.Summary, error) {
	root := snapshot.Manifest.Paths.Resolved().DeployRoot
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return hostpolicy.Summary{}, err
	}
	defer transport.Close()
	output, err := transport.Run(ctx, []string{path.Join(root, "host", "current", "bin", "moox-runtime"), "host-policy-status", "--root", root}, nil)
	if err != nil {
		return hostpolicy.Summary{}, fmt.Errorf("host-policy-status failed: %w: %s", err, strings.TrimSpace(output.Stderr))
	}
	var summary hostpolicy.Summary
	if err := json.Unmarshal([]byte(output.Stdout), &summary); err != nil {
		return hostpolicy.Summary{}, errors.New("host-policy-status returned invalid output")
	}
	return summary, nil
}
