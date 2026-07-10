package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func (s *Service) ListHostAgents(ctx context.Context, _ *monitorpb.ListHostAgentsReq) (*monitorpb.ListHostAgentsRsp, error) {
	store := hostmetrics.NewStore(s.db)
	if err := store.EnsureSchema(); err != nil {
		return &monitorpb.ListHostAgentsRsp{RetInfo: inner(err)}, nil
	}
	rows, err := store.ListAgents(ctx)
	if err != nil {
		return &monitorpb.ListHostAgentsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.HostAgentInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, &monitorpb.HostAgentInfo{AgentId: row.AgentID, Hostname: row.Hostname, BootId: row.BootID, LastSeenAt: row.LastSeenAt, Archived: row.Archived, Snapshot: row.Snapshot})
	}
	return &monitorpb.ListHostAgentsRsp{RetInfo: success(), Agents: out}, nil
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
	store := hostmetrics.NewStore(s.db)
	if err := store.EnsureSchema(); err != nil {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: inner(err)}, nil
	}
	rows, err := store.History(ctx, req.GetAgentId(), start, end, int(req.GetLimit()))
	if err != nil {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*monitorpb.HostMetricHistoryPoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, &monitorpb.HostMetricHistoryPoint{AgentId: row.AgentID, ObservedAt: row.ObservedAt, Snapshot: row.Snapshot})
	}
	return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: success(), Points: out}, nil
}
