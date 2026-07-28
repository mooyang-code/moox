package rpc

import (
	"context"
	"errors"

	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func (s *Service) GetObservabilityOverview(ctx context.Context, req *monitorpb.GetObservabilityOverviewReq) (*monitorpb.GetObservabilityOverviewRsp, error) {
	if req.GetSpaceId() == "" {
		return &monitorpb.GetObservabilityOverviewRsp{RetInfo: invalid(errors.New("space_id is required"))}, nil
	}
	if s.observabilityOverview == nil {
		return &monitorpb.GetObservabilityOverviewRsp{RetInfo: inner(errors.New("observability overview is unavailable"))}, nil
	}
	overview, err := s.observabilityOverview.Build(ctx, req.GetSpaceId())
	if err != nil {
		return &monitorpb.GetObservabilityOverviewRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.GetObservabilityOverviewRsp{RetInfo: success(), Overview: overviewToPB(overview)}, nil
}

func overviewToPB(value monitorobservability.Overview) *monitorpb.ObservabilityOverview {
	out := &monitorpb.ObservabilityOverview{
		GeneratedAt:    timeToString(value.GeneratedAt),
		Services:       make([]*monitorpb.ServiceObservabilityStatus, 0, len(value.Services)),
		Hosts:          make([]*monitorpb.HostObservabilityStatus, 0, len(value.Hosts)),
		Datasets:       make([]*monitorpb.DatasetFrequencyStatus, 0, len(value.Datasets)),
		BusinessChecks: make([]*monitorpb.BusinessObservabilityStatus, 0, len(value.BusinessChecks)),
		Scf: &monitorpb.ScfObservabilitySummary{
			OnlineCount: int32(value.SCF.OnlineCount), TimeoutCount: int32(value.SCF.TimeoutCount),
			UnknownCount: int32(value.SCF.UnknownCount), OldestHeartbeatAt: timeToString(value.SCF.OldestHeartbeatAt),
		},
	}
	for _, item := range value.Services {
		out.Services = append(out.Services, &monitorpb.ServiceObservabilityStatus{
			NodeId: item.NodeID, ServiceName: item.ServiceName, InstanceId: item.InstanceID,
			Status: item.Status, Reason: item.Reason, LastSeenAt: timeToString(item.LastSeenAt),
		})
	}
	for _, item := range value.Hosts {
		out.Hosts = append(out.Hosts, &monitorpb.HostObservabilityStatus{
			AgentId: item.AgentID, Hostname: item.Hostname, Status: item.Status, Reason: item.Reason,
			LastSeenAt: timeToString(item.LastSeenAt), CpuPercent: item.CPUPercent,
			MemoryPercent: item.MemoryPercent, FilesystemMaxPercent: item.FilesystemMaxPercent,
		})
	}
	for _, item := range value.Datasets {
		out.Datasets = append(out.Datasets, &monitorpb.DatasetFrequencyStatus{
			Producer: item.Producer, SpaceId: item.SpaceID, DatasetId: item.DatasetID, Freq: item.Freq,
			Status: item.Status, Reason: item.Reason, LastRunAt: timeToString(item.LastRunAt),
			LastSuccessAt: timeToString(item.LastSuccessAt), InputWatermarkAt: timeToString(item.InputWatermarkAt),
			OutputWatermarkAt: timeToString(item.OutputWatermarkAt), LagSeconds: item.LagSeconds,
		})
	}
	for _, item := range value.BusinessChecks {
		out.BusinessChecks = append(out.BusinessChecks, &monitorpb.BusinessObservabilityStatus{
			Kind: item.Kind, Module: item.Module, Status: item.Status,
			Reason: item.Reason, LastCheckedAt: timeToString(item.LastCheckedAt),
		})
	}
	return out
}
