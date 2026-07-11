package rpc

import (
	"context"
	"errors"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func (s *Service) ListHostAgents(ctx context.Context, _ *monitorpb.ListHostAgentsReq) (*monitorpb.ListHostAgentsRsp, error) {
	store := s.hostStore
	if store == nil {
		return &monitorpb.ListHostAgentsRsp{RetInfo: inner(errors.New("host monitor is unavailable")), StorageAvailable: false}, nil
	}
	if err := store.EnsureSchema(); err != nil {
		return &monitorpb.ListHostAgentsRsp{RetInfo: inner(err), StorageAvailable: false}, nil
	}
	rows, err := store.ListAgents(ctx)
	if err != nil {
		return &monitorpb.ListHostAgentsRsp{RetInfo: inner(err), StorageAvailable: false}, nil
	}
	out := make([]*monitorpb.HostAgentInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, &monitorpb.HostAgentInfo{AgentId: row.AgentID, Hostname: row.Hostname, BootId: row.BootID, LastSeenAt: row.LastSeenAt, Archived: row.Archived, Snapshot: row.Snapshot})
	}
	available := s.hostStorageReady == nil || s.hostStorageReady()
	return &monitorpb.ListHostAgentsRsp{RetInfo: success(), Agents: out, StorageAvailable: available, DataGap: !available}, nil
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
	requestedStart, requestedEnd := start, end
	now := time.Now().UTC()
	windowStart := now.Add(-72 * time.Hour)
	truncated := requestedEnd.Sub(requestedStart) > 72*time.Hour || requestedStart.Before(windowStart) || requestedEnd.After(now)
	if end.After(now) {
		end = now
	}
	if start.Before(windowStart) {
		start = windowStart
	}
	if end.Sub(start) > 72*time.Hour {
		start = end.Add(-72 * time.Hour)
	}
	if s.hostReader == nil {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: inner(errors.New("host history storage is unavailable")), StorageAvailable: false}, nil
	}
	if end.Before(start) {
		available := s.hostStorageReady == nil || s.hostStorageReady()
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: success(), StorageAvailable: available, DataGap: true}, nil
	}
	rows, err := s.hostReader.History(ctx, req.GetAgentId(), start, end, int(req.GetLimit()))
	if err != nil {
		return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: inner(err), StorageAvailable: false}, nil
	}
	out := make([]*monitorpb.HostMetricHistoryPoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, &monitorpb.HostMetricHistoryPoint{AgentId: row.AgentID, ObservedAt: row.ObservedAt, Snapshot: row.Snapshot})
	}
	available := s.hostStorageReady == nil || s.hostStorageReady()
	return &monitorpb.QueryHostMetricHistoryRsp{RetInfo: success(), Points: out, StorageAvailable: available, DataGap: !available || truncated || len(out) == 0}, nil
}
