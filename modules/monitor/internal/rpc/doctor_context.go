package rpc

import (
	"context"
	"errors"
	"time"

	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func (s *Service) GetDoctorContext(ctx context.Context, req *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error) {
	if err := req.Validate(); err != nil {
		return &monitorpb.GetDoctorContextRsp{RetInfo: invalid(err)}, nil
	}
	if s.doctorContext == nil {
		return &monitorpb.GetDoctorContextRsp{RetInfo: inner(errors.New("doctor context is unavailable"))}, nil
	}
	value, err := s.doctorContext.Build(ctx, req.GetNodeId(), req.GetComponentIds(), req.GetPipelineIds())
	if err != nil {
		return &monitorpb.GetDoctorContextRsp{RetInfo: invalid(err)}, nil
	}
	rsp := contextToPB(value)
	if err := validateDoctorResponseSize(rsp); err != nil {
		return &monitorpb.GetDoctorContextRsp{RetInfo: inner(errors.New("doctor context exceeds 2 MiB response limit"))}, nil
	}
	return rsp, nil
}

func validateDoctorResponseSize(rsp *monitorpb.GetDoctorContextRsp) error {
	if proto.Size(rsp) > monitorpb.MaxDoctorContextBytes {
		return errors.New("doctor context exceeds 2 MiB response limit")
	}
	encoded, err := protojson.Marshal(rsp)
	if err != nil {
		return err
	}
	if len(encoded) > monitorpb.MaxDoctorContextBytes {
		return errors.New("doctor context exceeds 2 MiB JSON response limit")
	}
	return nil
}

func contextToPB(value monitordoctor.Context) *monitorpb.GetDoctorContextRsp {
	rsp := &monitorpb.GetDoctorContextRsp{RetInfo: success(), GeneratedAt: value.GeneratedAt, ManifestChecksum: value.ManifestChecksum}
	for _, item := range value.ExpectedComponents {
		rsp.ExpectedComponents = append(rsp.ExpectedComponents, &monitorpb.DoctorExpectedComponent{ComponentId: item.ComponentID, ServiceName: item.ServiceName, NodeId: item.NodeID, Expected: item.Expected, DeploymentStatus: item.DeploymentStatus, Transport: item.Transport, FunctionalObservability: item.FunctionalObservability, HealthUrl: item.HealthURL})
	}
	for _, item := range value.HealthObservations {
		rsp.HealthObservations = append(rsp.HealthObservations, observationToPB(item))
	}
	for _, item := range value.ReporterObservations {
		rsp.ReporterObservations = append(rsp.ReporterObservations, observationToPB(item))
	}
	for _, item := range value.ModuleObservations {
		rsp.ModuleObservations = append(rsp.ModuleObservations, observationToPB(item))
	}
	for _, item := range value.MissingObservations {
		rsp.MissingObservations = append(rsp.MissingObservations, observationToPB(item))
	}
	for _, item := range value.Watermarks {
		rsp.Watermarks = append(rsp.Watermarks, &monitorpb.DoctorWatermark{Module: item.Module, Stage: item.Stage, Pipeline: item.Pipeline, ObservedAt: formatDoctorTime(item.ObservedAt), Value: item.Value, Status: item.Status})
	}
	for _, host := range value.Hosts {
		rsp.HostResources = append(rsp.HostResources, &monitorpb.HostAgentInfo{
			AgentId: host.AgentID, Hostname: host.Hostname, BootId: host.BootID,
			LastSeenAt: host.LastSeenAt, Archived: host.Archived, Snapshot: host.Snapshot,
			Reachable: host.Reachable, StaleSeconds: host.StaleSeconds,
		})
		for _, forecast := range value.Forecasts[host.AgentID] {
			rsp.DiskForecasts = append(rsp.DiskForecasts, &monitorpb.DoctorDiskForecast{NodeId: host.Hostname, AgentId: host.AgentID, Mountpoint: forecast.Mountpoint, Status: forecast.Status, GrowthBytesPerDay: forecast.GrowthBytesPerDay, RemainingDays: forecast.RemainingDays, ValidIntervals: uint32(forecast.ValidIntervals), Summary: forecast.Summary})
		}
	}
	for _, event := range value.Alerts {
		rsp.ActiveAlerts = append(rsp.ActiveAlerts, eventToPB(event))
	}
	return rsp
}

func observationToPB(item monitordoctor.Observation) *monitorpb.DoctorObservation {
	return &monitorpb.DoctorObservation{Kind: item.Kind, ComponentId: item.ComponentID, ServiceName: item.ServiceName, InstanceId: item.InstanceID, NodeId: item.NodeID, BootId: item.BootID, Status: item.Status, ObservedAt: formatDoctorTime(item.ObservedAt), Summary: item.Summary, DetailsJson: item.DetailsJSON, Stale: item.Stale, Conflict: item.Conflict, Value: item.Value, AgeSeconds: item.AgeSeconds, IntervalSeconds: int32(item.IntervalSeconds)}
}

func formatDoctorTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
