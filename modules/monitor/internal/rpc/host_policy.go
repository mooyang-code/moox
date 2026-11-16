package rpc

import (
	"context"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

// GetHostPolicy 返回各主机缓存的主机策略：全部告警渠道（含完整 Webhook URL 与密钥）。
// 只允许调用方 host-agent（网关 ACL 限定），HostAgent 定期拉取后写入 <部署根>/data/host-agent/host-policy.json，
// HostAgent 的对端检查与证书巡检在控制面不可用时也能直接推送告警。
func (s *Service) GetHostPolicy(ctx context.Context, _ *monitorpb.GetHostPolicyReq) (*monitorpb.GetHostPolicyRsp, error) {
	if s.notifications == nil {
		return &monitorpb.GetHostPolicyRsp{RetInfo: inner(errNotificationUnavailable)}, nil
	}
	channels, err := s.notifications.List(ctx)
	if err != nil {
		return &monitorpb.GetHostPolicyRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.HostPolicyChannel, 0, len(channels))
	for _, channel := range channels {
		out = append(out, &monitorpb.HostPolicyChannel{ChannelType: channel.ChannelType, WebhookUrl: channel.WebhookURL, Secret: channel.Secret})
	}
	startedAt := ""
	if !s.startedAt.IsZero() {
		startedAt = s.startedAt.UTC().Format(time.RFC3339Nano)
	}
	return &monitorpb.GetHostPolicyRsp{RetInfo: success(), Channels: out, MonitorStartedAt: startedAt}, nil
}
