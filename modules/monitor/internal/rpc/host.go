package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func (s *Service) ListHostAgents(ctx context.Context, _ *monitorpb.ListHostAgentsReq) (*monitorpb.ListHostAgentsRsp, error) {
	store := s.hostStore
	if store == nil {
		return &monitorpb.ListHostAgentsRsp{RetInfo: inner(errors.New("主机监控未初始化"))}, nil
	}
	rows, err := store.ListAgents(ctx)
	if err != nil {
		return &monitorpb.ListHostAgentsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.HostAgentInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, &monitorpb.HostAgentInfo{
			HostId: row.HostID, AgentId: row.AgentID, Hostname: row.Hostname, BootId: row.BootID,
			LastSeenAt: row.LastSeenAt, Archived: row.Archived, Snapshot: row.Snapshot,
			Reachable: row.Reachable, StaleSeconds: row.StaleSeconds,
		})
	}
	return &monitorpb.ListHostAgentsRsp{RetInfo: success(), Agents: out}, nil
}

// historyClockSlack 是判断请求范围是否超出可查范围时容许的时钟差。
const historyClockSlack = 5 * time.Minute

func (s *Service) QueryHostMetricHistory(ctx context.Context, req *monitorpb.QueryHostMetricHistoryReq) (*monitorpb.QueryHostMetricHistoryRsp, error) {
	if req.GetAgentId() == "" {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: invalid(errors.New("缺少 agent_id"))}, nil
	}
	end := time.Now().UTC()
	start := end.Add(-time.Hour)
	var err error
	if req.GetStartAt() != "" {
		start, err = time.Parse(time.RFC3339Nano, req.GetStartAt())
		if err != nil {
			return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: invalid(err)}, nil
		}
	}
	if req.GetEndAt() != "" {
		end, err = time.Parse(time.RFC3339Nano, req.GetEndAt())
		if err != nil {
			return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: invalid(err)}, nil
		}
	}
	if end.Before(start) {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: invalid(errors.New("end_at 不能早于 start_at"))}, nil
	}
	// 可查范围是快照历史的保留期（最近 7 天）；超出的部分截掉，并在 data_gap 里标明。
	// 判断是否超出时留 historyClockSlack 的余量：页面按自己的时钟算“最近 7 天”，不因两边时钟差几秒就报缺口。
	requestedStart, requestedEnd := start, end
	now := time.Now().UTC()
	windowStart := now.Add(-hostmetrics.SampleRetention)
	truncated := requestedStart.Before(windowStart.Add(-historyClockSlack)) || requestedEnd.After(now.Add(historyClockSlack))
	if end.After(now) {
		end = now
	}
	if start.Before(windowStart) {
		start = windowStart
	}
	if s.hostStore == nil {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: inner(errors.New("主机监控未初始化"))}, nil
	}
	if end.Before(start) {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: success(), DataGap: true}, nil
	}
	rows, err := s.hostStore.History(ctx, req.GetAgentId(), start, end, int(req.GetLimit()))
	if err != nil {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.HostMetricHistoryPoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, &monitorpb.HostMetricHistoryPoint{AgentId: row.AgentID, ObservedAt: row.ObservedAt, Snapshot: row.Snapshot})
	}
	return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: success(), Points: out, DataGap: truncated || len(out) == 0}, nil
}
