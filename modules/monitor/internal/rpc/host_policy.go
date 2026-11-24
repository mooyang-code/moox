package rpc

import (
	"context"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/log"
)

// GetHostPolicy 收下 HostAgent 带来的主机快照，返回各主机缓存的主机策略：全部告警渠道（含完整 Webhook URL 与密钥）。
// 只允许调用方 host-agent（网关 ACL 限定），HostAgent 每分钟调用一次，策略写入 <部署根>/data/host-agent/host-policy.json，
// HostAgent 的对端检查与证书巡检在控制面不可用时也能直接推送告警。
//
// 先收快照再判评估停滞：评估停了主机快照也照常入库。快照不合法或写库失败只记日志（写库成败另记进就绪判断），策略照常返回，
// 否则 HostAgent 会把它当成 Monitor 不可达。
// 检查评估停滞（进程活着，但检查不再产生结果，所有告警静默失效）时返回失败：HostAgent 连续 3 次拉取失败会直推
// “控制机或 Monitor 不可达”，这是评估停滞唯一能送到人的途径；HostAgent 拉取失败时保留旧的策略缓存。
func (s *Service) GetHostPolicy(ctx context.Context, req *monitorpb.GetHostPolicyReq) (*monitorpb.GetHostPolicyRsp, error) {
	if report := req.GetHost(); report != nil && s.hostReports != nil {
		if err := s.hostReports(ctx, report); err != nil {
			log.WarnContextf(ctx, "主机 %s 的快照没有收下: %v", report.GetMetric().GetHostId(), err)
		}
	}
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
