package rpc

import (
	"context"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

// GetHostPolicy 返回各主机缓存的主机策略：全部告警渠道（含完整 Webhook URL）与主机守护参数。
// 只允许调用方 host-agent（网关 ACL 限定），HostAgent 定期拉取后写入 <部署根>/run/host-policy.json，
// 主机守护与证书巡检在控制面不可用时也能直接推送告警。
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
		out = append(out, &monitorpb.HostPolicyChannel{ChannelType: channel.ChannelType, WebhookUrl: channel.WebhookURL})
	}
	w := s.hostWatchdog
	return &monitorpb.GetHostPolicyRsp{RetInfo: success(), Channels: out, Watchdog: &monitorpb.HostWatchdogPolicy{
		Disabled:          w.Disabled,
		MergeWindow:       durationText(w.MergeWindow),
		CrashLoopFailures: int32(w.CrashLoopFailures),
		CrashLoopWindow:   durationText(w.CrashLoopWindow),
		CrashLoopRetry:    durationText(w.CrashLoopRetry),
		CrashLoopSlow:     durationText(w.CrashLoopSlow),
		StableAfter:       durationText(w.StableAfter),
		NotReadyAlert:     durationText(w.NotReadyAlert),
	}}, nil
}

// durationText 把时长写成 Go 的写法；零值为空串，表示主机使用默认值。
func durationText(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}
