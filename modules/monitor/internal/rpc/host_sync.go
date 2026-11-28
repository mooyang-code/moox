package rpc

import (
	"context"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/log"
)

// SyncHostAgent 收下 HostAgent 带来的主机快照，返回各主机缓存的全部告警渠道（含完整 Webhook URL 与密钥）及 Monitor 启动时刻。
// 只允许调用方 host-agent（网关 ACL 限定），HostAgent 每分钟调用一次，渠道写入 <部署根>/data/host-agent/host-policy.json，
// 供非控制机在控制机或 Monitor 不可达、Monitor 反复重启时直接推送告警。
//
// 先收快照再判评估停滞：评估停了主机快照也照常入库。快照不合法或写库失败只记日志（写库成败另记进就绪判断），渠道照常返回，
// 否则 HostAgent 会把它当成 Monitor 不可达。
// 检查评估停滞（进程活着，但检查不再产生结果，所有告警静默失效）时返回失败：HostAgent 连续 3 次同步失败会直推
// “控制机或 Monitor 不可达”，这是评估停滞唯一能送到人的途径；HostAgent 同步失败时保留旧的策略缓存。
func (s *Service) SyncHostAgent(ctx context.Context, req *monitorpb.SyncHostAgentReq) (*monitorpb.SyncHostAgentRsp, error) {
	if report := req.GetHost(); report != nil && s.hostReports != nil {
		if err := s.hostReports(ctx, report); err != nil {
			log.WarnContextf(ctx, "主机 %s 的快照没有收下: %v", report.GetMetric().GetHostId(), err)
		}
	}
	if s.evalStale != nil {
		if reason := s.evalStale(); reason != "" {
			return &monitorpb.SyncHostAgentRsp{RetInfo: retInfo(commonpb.ErrorCode_INNER_ERR, "Monitor 检查评估停滞："+reason)}, nil
		}
	}
	if s.notifications == nil {
		return &monitorpb.SyncHostAgentRsp{RetInfo: inner(errNotificationUnavailable)}, nil
	}
	channels, err := s.notifications.List(ctx)
	if err != nil {
		return &monitorpb.SyncHostAgentRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.HostNotificationChannel, 0, len(channels))
	for _, channel := range channels {
		out = append(out, &monitorpb.HostNotificationChannel{ChannelType: channel.ChannelType, WebhookUrl: channel.WebhookURL, Secret: channel.Secret})
	}
	return &monitorpb.SyncHostAgentRsp{RetInfo: success(), Channels: out, MonitorStartedAt: millis(s.startedAt)}, nil
}
