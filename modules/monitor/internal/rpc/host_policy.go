package rpc

import (
	"context"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// GetHostPolicy 返回各主机缓存的主机策略：全部告警渠道（含完整 Webhook URL 与密钥）。
// 只允许调用方 host-agent（网关 ACL 限定），HostAgent 定期拉取后写入 <部署根>/data/host-agent/host-policy.json，
// HostAgent 的对端检查与证书巡检在控制面不可用时也能直接推送告警。
// 检查评估停滞（进程活着，但检查不再产生结果，所有告警静默失效）时返回失败：HostAgent 连续 3 次拉取失败会直推
// “控制机或 Monitor 不可达”，这是评估停滞唯一能送到人的途径；HostAgent 拉取失败时保留旧的策略缓存。
func (s *Service) GetHostPolicy(ctx context.Context, _ *monitorpb.GetHostPolicyReq) (*monitorpb.GetHostPolicyRsp, error) {
	if s.evalStale != nil {
		if reason := s.evalStale(); reason != "" {
			return &monitorpb.GetHostPolicyRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, "Monitor 检查评估停滞："+reason)}, nil
		}
	}
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
