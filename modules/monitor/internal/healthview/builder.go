package healthview

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

type Builder struct {
	Facts           *observability.Builder
	Checks          *store.CheckRepository
	Results         *store.ResultRepository
	Alerts          *store.AlertRepository
	ComponentHealth *store.ComponentHealthRepository
	Notifications   *store.NotificationRepository
	Now             func() time.Time
}

func stamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (b Builder) Build(ctx context.Context, spaceID string) (*Overview, error) {
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	facts := observability.Overview{GeneratedAt: now}
	if b.Facts != nil {
		var err error
		facts, err = b.Facts.Build(ctx, spaceID)
		if err != nil {
			return nil, err
		}
	}
	out := projectFacts(facts)
	out.GeneratedAt = stamp(now)
	if b.ComponentHealth != nil {
		states, err := b.ComponentHealth.List(ctx)
		if err != nil {
			return nil, err
		}
		byKey := make(map[string]domain.ComponentHealthState, len(states))
		for _, state := range states {
			byKey[state.HostID+"\x00"+state.ComponentID] = state
		}
		for _, component := range out.Components {
			if state, ok := byKey[component.HostId+"\x00"+component.ComponentId]; ok && state.Status == component.Status {
				component.StatusSince = stamp(state.SinceAt)
			}
		}
	}
	titles := newCheckTitles(b.Checks)
	active := make(map[incidentKey]bool)
	if b.Alerts != nil {
		states, err := b.Alerts.ListEnabledFiringStates(ctx, spaceID, 1501)
		if err != nil {
			return nil, err
		}
		if len(states) > 1500 {
			return nil, fmt.Errorf("health overview exceeds 1500 active alerts")
		}
		for _, state := range states {
			alert, err := b.projectAlert(ctx, state, facts, titles)
			if err != nil {
				return nil, err
			}
			out.Alerts = append(out.Alerts, alert)
			active[incidentKey{space: state.SpaceID, check: state.CheckID}] = true
		}
		out.Incidents, err = b.incidents(ctx, spaceID, facts, active, titles, now)
		if err != nil {
			return nil, err
		}
		out.IncidentWindowHours = int32(IncidentWindow / time.Hour)
	}
	if b.Notifications != nil {
		channels, err := b.Notifications.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, channel := range channels {
			out.Notification.ChannelTypes = append(out.Notification.ChannelTypes, channel.ChannelType)
		}
		out.Notification.ChannelCount = int32(len(channels))
	}
	sort.Slice(out.Alerts, func(i, j int) bool {
		a, z := out.Alerts[i], out.Alerts[j]
		if a.TriggeredAt != z.TriggeredAt {
			return a.TriggeredAt > z.TriggeredAt
		}
		return a.Id < z.Id
	})
	out.Summary.AlertCount = int32(len(out.Alerts))
	out.System = projectSystem(out.Components)
	return out, nil
}

func projectFacts(facts observability.Overview) *Overview {
	out := &Overview{GeneratedAt: stamp(facts.GeneratedAt), TopologyKnown: facts.TopologyKnown, Summary: &pb.HealthSummary{Components: &pb.HealthCounts{}}, Notification: &pb.NotificationSummary{}}
	for _, service := range facts.Services {
		component := projectComponent(service, facts)
		out.Components = append(out.Components, component)
		countStatus(out.Summary.Components, component.Status)
		// A health:none component can still have a missing/stale reporter.
		if component.Status == "unchecked" && service.ReporterStatus != "" && service.ReporterStatus != "healthy" {
			out.Summary.AttentionCount++
		}
		countSummary(out.Summary, component.Status)
	}
	for _, service := range facts.Unregistered {
		component := projectComponent(service, facts)
		component.Status, component.Reason = "degraded", "进程在上报，但没有部署记录"
		out.Unregistered = append(out.Unregistered, component)
	}
	out.Summary.UnregisteredCount = int32(len(out.Unregistered))
	out.Summary.AttentionCount += out.Summary.UnregisteredCount
	for _, item := range facts.BusinessChecks {
		status := domain.HealthStatus(item.Status)
		checkID := item.CheckID
		if checkID == "" {
			checkID = item.Kind + ":" + item.Module
		}
		out.BusinessChecks = append(out.BusinessChecks, &pb.HealthBusinessCheck{CheckId: checkID, Name: item.Name, Kind: item.Kind, Module: item.Module, SpaceId: item.SpaceID, Status: status, Reason: ChineseReason(item.Reason), RawError: unhealthyRaw(status, item.Reason, item.RawError), CheckedAt: stamp(item.LastCheckedAt)})
		countSummary(out.Summary, status)
	}
	out.Pipeline = projectPipeline(facts)
	for _, stage := range out.Pipeline {
		for _, dataset := range stage.Datasets {
			countSummary(out.Summary, dataset.Status)
		}
	}
	out.Hosts = projectHosts(facts)
	for _, host := range out.Hosts {
		countSummary(out.Summary, host.Status)
	}
	sort.Slice(out.Components, func(i, j int) bool {
		a, z := out.Components[i], out.Components[j]
		return a.HostId+"\x00"+a.ComponentId < z.HostId+"\x00"+z.ComponentId
	})
	return out
}

func componentName(facts observability.Overview, id string) string {
	if facts.Topology != nil {
		if component, ok := facts.Topology.Catalog.Component(id); ok {
			return component.Name
		}
	}
	return id
}

func projectComponent(item observability.ServiceStatus, facts observability.Overview) *pb.HealthComponent {
	reporterStatus, reporterReason := "unchecked", "未配置指标上报"
	switch item.ReporterStatus {
	case "healthy":
		reporterStatus, reporterReason = "healthy", "监控上报正常"
	case "stale":
		reporterStatus, reporterReason = "degraded", "监控上报已中断"
	case "missing":
		reporterStatus, reporterReason = "unknown", "从未收到监控上报"
	}
	component := &pb.HealthComponent{HostId: item.NodeID, ComponentId: item.ServiceName, Name: componentName(facts, item.ServiceName), Status: domain.HealthStatus(item.Status), Reason: ChineseReason(item.Reason),
		Reporter:  &pb.HealthSignal{Status: reporterStatus, Reason: reporterReason, CheckedAt: stamp(item.LastSeenAt)},
		Readiness: item.Readiness, NotReadyReason: item.NotReadyReason}
	component.Lifecycle = projectLifecycle((*lifecycleSource)(item.Lifecycle))
	for _, instance := range item.Instances {
		component.Instances = append(component.Instances, &pb.HealthReporter{InstanceId: instance.InstanceID, BootId: instance.BootID, Version: instance.Version, Status: domain.HealthStatus(instance.Status), LastReportedAt: stamp(instance.LastSeenAt)})
	}
	return component
}

func rawReason(reason, raw string) string {
	if raw != "" {
		return raw
	}
	if ChineseReason(reason) != reason {
		return reason
	}
	return ""
}

func countStatus(counts *pb.HealthCounts, status string) {
	switch status {
	case "healthy":
		counts.HealthyCount++
	case "degraded", "down":
		counts.AttentionCount++
	case "unknown":
		counts.UnknownCount++
	case "disabled":
		counts.DisabledCount++
	case "unchecked":
		counts.UncheckedCount++
	}
}

func countSummary(summary *pb.HealthSummary, status string) {
	switch status {
	case "healthy":
		summary.HealthyCount++
	case "degraded", "down":
		summary.AttentionCount++
	case "unknown":
		summary.UnknownCount++
	}
}

func unhealthyRaw(status, reason, raw string) string {
	if status == "healthy" || status == "disabled" || status == "unchecked" {
		return ""
	}
	return rawReason(reason, raw)
}
