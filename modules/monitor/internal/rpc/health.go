package rpc

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func (s *Service) GetHealthOverview(ctx context.Context, req *monitorpb.GetHealthOverviewReq) (*monitorpb.GetHealthOverviewRsp, error) {
	if s.healthView == nil {
		return &monitorpb.GetHealthOverviewRsp{RetInfo: inner(errors.New("健康概览不可用"))}, nil
	}
	overview, err := s.healthView.Build(ctx, req.GetSpaceId())
	if err != nil {
		return &monitorpb.GetHealthOverviewRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.GetHealthOverviewRsp{RetInfo: success(), Overview: healthOverviewToPB(overview)}, nil
}

func healthOverviewToPB(value healthview.Overview) *monitorpb.HealthOverview {
	out := &monitorpb.HealthOverview{
		GeneratedAt: timeToString(value.GeneratedAt),
		Summary: &monitorpb.HealthSummary{
			Alerts: int32(value.Summary.Alerts), Attention: int32(value.Summary.Attention),
			Healthy: int32(value.Summary.Healthy), Unknown: int32(value.Summary.Unknown),
		},
		Alerts:         make([]*monitorpb.HealthAlert, 0, len(value.Alerts)),
		Components:     make([]*monitorpb.HealthComponent, 0, len(value.Components)),
		DataStages:       make([]*monitorpb.HealthDataStage, 0, len(value.DataStages)),
		BusinessChecks: make([]*monitorpb.HealthBusinessCheck, 0, len(value.BusinessChecks)),
		Hosts:          make([]*monitorpb.HealthHost, 0, len(value.Hosts)),
		Unregistered:   make([]*monitorpb.HealthUnregistered, 0, len(value.Unregistered)),
		Notification: &monitorpb.HealthNotification{
			ChannelType: value.Notification.ChannelType, Configured: value.Notification.Configured,
			WebhookMasked: value.Notification.WebhookMasked,
		},
		Warnings: append([]string{}, value.Warnings...),
	}
	for _, item := range value.Alerts {
		out.Alerts = append(out.Alerts, &monitorpb.HealthAlert{
			Id: item.ID, Severity: item.Severity, Title: item.Title, Reason: item.Reason, RawError: item.RawError,
			Target: &monitorpb.HealthTarget{
				Kind: item.Target.Kind, HostId: item.Target.HostID, ComponentId: item.Target.ComponentID,
				SpaceId: item.Target.SpaceID, DatasetId: item.Target.DatasetID, Frequency: item.Target.Frequency,
			},
			TriggeredAt: timeToString(item.TriggeredAt), LastCheckedAt: timeToString(item.LastCheckedAt),
			Stage: item.Stage,
		})
	}
	for _, item := range value.Components {
		component := &monitorpb.HealthComponent{
			HostId: item.HostID, ComponentId: item.ComponentID, Name: item.Name, Status: item.Status, Reason: item.Reason,
			Probe: healthProbeToPB(item.Probe), History: make([]*monitorpb.HealthProbe, 0, len(item.History)),
			Reporter: &monitorpb.HealthReporter{
				Status: item.Reporter.Status, InstanceId: item.Reporter.InstanceID, Version: item.Reporter.Version,
				LastSeenAt: timeToString(item.Reporter.LastSeenAt),
			},
			StatusSince: timeToString(item.StatusSince),
		}
		for _, probe := range item.History {
			component.History = append(component.History, healthProbeToPB(probe))
		}
		out.Components = append(out.Components, component)
	}
	for _, stage := range value.DataStages {
		item := &monitorpb.HealthDataStage{
			Stage: stage.Stage, Name: stage.Name, Status: stage.Status,
			Datasets: make([]*monitorpb.HealthStageDataset, 0, len(stage.Datasets)),
		}
		for _, dataset := range stage.Datasets {
			item.Datasets = append(item.Datasets, &monitorpb.HealthStageDataset{
				SpaceId: dataset.SpaceID, DatasetId: dataset.DatasetID, Frequency: dataset.Frequency,
				Producer: dataset.Producer, Status: dataset.Status, WatermarkAt: timeToString(dataset.WatermarkAt),
				LagSeconds: dataset.LagSeconds, LastSuccessAt: timeToString(dataset.LastSuccessAt),
				Reason: dataset.Reason, RawError: dataset.RawError,
			})
		}
		out.DataStages = append(out.DataStages, item)
	}
	for _, item := range value.BusinessChecks {
		out.BusinessChecks = append(out.BusinessChecks, &monitorpb.HealthBusinessCheck{
			Kind: item.Kind, Name: item.Name, Module: item.Module, SpaceId: item.SpaceID, Status: item.Status,
			Reason: item.Reason, RawError: item.RawError, CheckedAt: timeToString(item.CheckedAt), Stage: item.Stage,
		})
	}
	for _, item := range value.Hosts {
		out.Hosts = append(out.Hosts, &monitorpb.HealthHost{
			HostId: item.HostID, AgentId: item.AgentID, Status: item.Status, Reason: item.Reason,
			GatewayState: item.GatewayState, CpuPercent: item.CPUPercent, MemoryPercent: item.MemoryPercent,
			DiskPercent: item.DiskPercent, LastSeenAt: timeToString(item.LastSeenAt),
		})
	}
	for _, item := range value.Unregistered {
		out.Unregistered = append(out.Unregistered, &monitorpb.HealthUnregistered{
			HostId: item.HostID, ComponentId: item.ComponentID, InstanceId: item.InstanceID,
			Version: item.Version, LastSeenAt: timeToString(item.LastSeenAt),
		})
	}
	return out
}

func healthProbeToPB(value healthview.Probe) *monitorpb.HealthProbe {
	return &monitorpb.HealthProbe{
		Status: value.Status, Url: value.URL, CheckedAt: timeToString(value.CheckedAt), RawError: value.RawError,
	}
}
