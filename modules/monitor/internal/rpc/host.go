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
		return &monitorpb.ListHostAgentsRsp{RetInfo: inner(errors.New("host monitor is unavailable")), StorageAvailable: false}, nil
	}
	rows, err := store.ListAgents(ctx)
	if err != nil {
		return &monitorpb.ListHostAgentsRsp{RetInfo: inner(err), StorageAvailable: false}, nil
	}
	out := make([]*monitorpb.HostAgentInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, &monitorpb.HostAgentInfo{
			HostId: row.HostID, AgentId: row.AgentID, Hostname: row.Hostname, BootId: row.BootID,
			LastSeenAt: row.LastSeenAt, Archived: row.Archived, Snapshot: row.Snapshot,
			Reachable: row.Reachable, StaleSeconds: row.StaleSeconds,
		})
	}
	return &monitorpb.ListHostAgentsRsp{RetInfo: success(), Agents: out, StorageAvailable: true}, nil
}

func (s *Service) QueryHostMetricHistory(ctx context.Context, req *monitorpb.QueryHostMetricHistoryReq) (*monitorpb.QueryHostMetricHistoryRsp, error) {
	if req.GetAgentId() == "" {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: invalid(errors.New("agent_id is required"))}, nil
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
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: invalid(errors.New("end_at must not precede start_at"))}, nil
	}
	// 可查范围是快照历史的保留期（最近 7 天）；超出的部分截掉，并在 data_gap 里标明。
	requestedStart, requestedEnd := start, end
	now := time.Now().UTC()
	window := hostmetrics.SampleRetention
	windowStart := now.Add(-window)
	truncated := requestedEnd.Sub(requestedStart) > window || requestedStart.Before(windowStart) || requestedEnd.After(now)
	if end.After(now) {
		end = now
	}
	if start.Before(windowStart) {
		start = windowStart
	}
	if s.hostStore == nil {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: inner(errors.New("host monitor is unavailable")), StorageAvailable: false}, nil
	}
	if end.Before(start) {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: success(), StorageAvailable: true, DataGap: true}, nil
	}
	rows, err := s.hostStore.History(ctx, req.GetAgentId(), start, end, int(req.GetLimit()))
	if err != nil {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: inner(err), StorageAvailable: false}, nil
	}
	out := make([]*monitorpb.HostMetricHistoryPoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, &monitorpb.HostMetricHistoryPoint{AgentId: row.AgentID, ObservedAt: row.ObservedAt, Snapshot: row.Snapshot})
	}
	return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: success(), Points: out, StorageAvailable: true, DataGap: truncated || len(out) == 0}, nil
}
