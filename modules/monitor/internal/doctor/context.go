// Package doctor assembles bounded Monitor facts for the interactive Doctor
// CLI. It does not execute checks or infer root causes.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/deployment"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
)

const (
	MaxObservations = 128
	MaxAlerts       = 100
	MaxSeries       = 256
)

type Builder struct {
	Deployments         deployment.Source
	Alerts              *store.AlertRepository
	Metrics             *monmetrics.QueryService
	Hosts               *hostmetrics.Store
	HealthChecks        []report.ModuleHealthCheck
	DatasetHealthPolicy report.DatasetHealthPolicy
	Now                 func() time.Time
}

type ExpectedComponent struct {
	ComponentID, ServiceName, NodeID, DeploymentStatus, Transport, FunctionalObservability string
	Expected                                                                               bool
	DeploymentCreatedAt                                                                    time.Time
}

type Observation struct {
	Kind, ComponentID, ServiceName, InstanceID, NodeID, BootID, Status, Summary, DetailsJSON string
	ObservedAt                                                                               time.Time
	Stale, Conflict                                                                          bool
	Value                                                                                    float64
	AgeSeconds                                                                               int64
	IntervalSeconds                                                                          int
}

type Watermark struct {
	Module, Stage, HealthCheckID, Status string
	ObservedAt                           time.Time
	Value                                float64
}

type Context struct {
	GeneratedAt, ManifestChecksum string
	ExpectedComponents            []ExpectedComponent
	HealthObservations            []Observation
	ReporterObservations          []Observation
	ModuleObservations            []Observation
	Watermarks                    []Watermark
	Hosts                         []hostmetrics.AgentView
	Forecasts                     map[string][]hostmetrics.DiskForecast
	Alerts                        []domain.AlertEvent
	MissingObservations           []Observation
}

func (b Builder) Build(ctx context.Context, nodeID string, componentIDs, healthCheckIDs []string) (Context, error) {
	manifest, err := doctor.LoadEmbeddedManifest()
	if err != nil {
		return Context{}, err
	}
	components, err := selectComponents(manifest.Components, componentIDs)
	if err != nil {
		return Context{}, err
	}
	if err := validateHealthChecks(b.HealthChecks, healthCheckIDs); err != nil {
		return Context{}, err
	}
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	out := Context{GeneratedAt: now.Format(time.RFC3339Nano), ManifestChecksum: manifest.Checksum, Forecasts: map[string][]hostmetrics.DiskForecast{}}
	snapshot, deploymentErr := b.loadDeployments(ctx)
	hosts := make(map[string]bool, len(snapshot.Hosts))
	for _, host := range snapshot.Hosts {
		hosts[host.GetHostId()] = host.GetStatus() == "enabled"
	}
	for _, component := range components {
		found := false
		for _, deployed := range snapshot.Deployments {
			if deployed.GetComponentId() != component.ComponentID || (nodeID != "" && deployed.GetHostId() != nodeID) {
				continue
			}
			found = true
			out.ExpectedComponents = append(out.ExpectedComponents, ExpectedComponent{
				ComponentID: component.ComponentID, ServiceName: component.ServiceName, NodeID: deployed.GetHostId(),
				Expected: hosts[deployed.GetHostId()] && deployed.GetStatus() == "enabled", DeploymentStatus: deployed.GetStatus(),
				Transport: string(component.Transport), FunctionalObservability: string(component.FunctionalObservability),
				DeploymentCreatedAt: parseTimestamp(deployed.GetCreatedAt()),
			})
		}
		if !found {
			out.ExpectedComponents = append(out.ExpectedComponents, ExpectedComponent{ComponentID: component.ComponentID, ServiceName: component.ServiceName, NodeID: nodeID, DeploymentStatus: "missing", Transport: string(component.Transport), FunctionalObservability: string(component.FunctionalObservability)})
		}
	}
	if deploymentErr != nil {
		out.MissingObservations = append(out.MissingObservations, Observation{Kind: "deployment", Status: "UNKNOWN", Summary: "部署事实暂不可用：" + deploymentErr.Error()})
	}
	if err := b.addHealth(ctx, components, now, &out); err != nil {
		return Context{}, err
	}
	if err := b.addMetrics(ctx, components, nodeID, healthCheckIDs, now, &out); err != nil {
		return Context{}, err
	}
	b.addIngestDiagnostics(ctx, now, &out)
	if err := b.addHosts(ctx, nodeID, now, &out); err != nil {
		out.MissingObservations = append(out.MissingObservations, Observation{Kind: "host", NodeID: nodeID, Status: "UNKNOWN", Summary: "host resource history unavailable"})
	}
	if b.Alerts != nil {
		events, listErr := b.Alerts.ListRecentEvents(ctx, MaxAlerts)
		err = listErr
		if err != nil {
			return Context{}, err
		}
		for _, event := range events {
			if event.Status == domain.AlertStatusFiring {
				out.Alerts = append(out.Alerts, event)
			}
		}
	}
	if err := enforceBounds(out); err != nil {
		return Context{}, err
	}
	return out, nil
}

// maxIngestObservations 限制每类入库诊断放进 Doctor 上下文的条数，避免挤占 module 观察项的上限。
const maxIngestObservations = 10

// addIngestDiagnostics 把指标入库的诊断放进上下文：哪些服务的新序列被限额丢弃、哪些服务的快照被拒收以及最近一次的原因。
// 诊断来自进程内累计，Monitor 重启后清零；不影响其他观察项，读取失败也不让 Doctor 失败。
func (b Builder) addIngestDiagnostics(ctx context.Context, now time.Time, out *Context) {
	if b.Metrics == nil {
		return
	}
	room := func() int { return min(maxIngestObservations, MaxObservations-len(out.ModuleObservations)) }
	if limit, err := b.Metrics.SeriesLimitStatus(ctx, now); err == nil && limit.Enabled {
		for _, drop := range limit.Drops {
			if room() <= 0 {
				break
			}
			details, _ := json.Marshal(map[string]any{"last_metric": drop.LastMetric, "total_dropped": drop.Total, "hour_current": limit.HourCurrent, "hour_max": limit.HourMax, "day_current": limit.DayCurrent, "day_max": limit.DayMax})
			out.ModuleObservations = append(out.ModuleObservations, Observation{
				Kind: "metrics_ingest", ComponentID: "monitor", ServiceName: drop.Service, Status: "DEGRADED",
				ObservedAt: drop.LastAt, Value: float64(drop.Total), AgeSeconds: observationAge(now, drop.LastAt),
				Summary: "metrics_ingest_series_dropped", DetailsJSON: string(details),
			})
		}
	}
	for i, reject := range b.Metrics.IngestRejects() {
		if i >= maxIngestObservations || room() <= 0 {
			break
		}
		details, _ := json.Marshal(map[string]any{"last_reason": reject.LastReason, "by_reason": reject.ByReason})
		out.ModuleObservations = append(out.ModuleObservations, Observation{
			Kind: "metrics_ingest", ComponentID: "monitor", ServiceName: reject.Service, Status: "DEGRADED",
			ObservedAt: reject.LastAt, AgeSeconds: observationAge(now, reject.LastAt),
			Summary: "metrics_ingest_snapshot_rejected", DetailsJSON: string(details),
		})
	}
}

func (b Builder) loadDeployments(ctx context.Context) (deployment.Snapshot, error) {
	if b.Deployments == nil {
		return deployment.Snapshot{}, fmt.Errorf("deployment source is unavailable")
	}
	return b.Deployments.Snapshot(ctx)
}

// addHealth 按各组件的上报生成健康观察项（组件健康只看上报，设计 3.8），来源跟目录里的 doctor.transport 一致：
//   - reporter：组件自己的上报，按主机加组件名匹配；
//   - delegated：管理端代报（因子引擎、归档 worker），代报行不带主机，按组件名匹配；
//   - host_snapshot：host-agent 的主机快照，按主机匹配，快照新鲜为 OK。
//
// 上报里的就绪判断：就绪为 OK，未就绪为 DOWN，没有就绪判断为 UNKNOWN；上报超过两个间隔为过期；没有上报时记为缺失。
func (b Builder) addHealth(ctx context.Context, components []doctor.Component, now time.Time, out *Context) error {
	componentByService := componentMap(components)
	names := make([]string, 0, len(components))
	snapshots := false
	for _, component := range components {
		switch component.Transport {
		case doctor.TransportReporter, doctor.TransportDelegated:
			names = append(names, component.ServiceName)
		case doctor.TransportHostSnapshot:
			snapshots = true
		}
	}
	latest := map[string]monmetrics.MetricService{}
	if b.Metrics != nil && b.Metrics.Catalog() != nil && len(names) > 0 {
		services, err := b.Metrics.Catalog().ListServicesForAt(ctx, names, "", MaxObservations, now)
		if err != nil {
			return err
		}
		for _, service := range services {
			key := service.ServiceName + "\x00" + service.NodeID
			if current, ok := latest[key]; !ok || service.LastSeenAt.After(current.LastSeenAt) {
				latest[key] = service
			}
		}
	}
	var agents []hostmetrics.AgentView
	var agentsErr error
	if snapshots && b.Hosts != nil {
		agents, agentsErr = b.Hosts.ListAgentsAt(ctx, now)
	}
	for _, expected := range out.ExpectedComponents {
		component := componentByService[expected.ServiceName]
		if !expected.Expected {
			continue
		}
		missing := Observation{Kind: "health", ComponentID: expected.ComponentID, ServiceName: expected.ServiceName, NodeID: expected.NodeID, Status: "MISSING", Summary: "health observation is missing"}
		switch component.Transport {
		case doctor.TransportHostSnapshot:
			if b.Hosts == nil || agentsErr != nil {
				missing.Status, missing.Summary = "UNKNOWN", "host snapshots are unavailable"
				out.MissingObservations = append(out.MissingObservations, missing)
				continue
			}
			agent, ok := latestAgent(agents, expected.NodeID)
			if !ok {
				out.MissingObservations = append(out.MissingObservations, missing)
				continue
			}
			seen := parseTimestamp(agent.LastSeenAt)
			summary := "host snapshot is fresh"
			if !agent.Reachable {
				summary = "host snapshot is stale"
			}
			out.HealthObservations = append(out.HealthObservations, Observation{
				Kind: "health", ComponentID: expected.ComponentID, ServiceName: expected.ServiceName, InstanceID: agent.AgentID,
				NodeID: expected.NodeID, BootID: agent.BootID, Status: "OK", ObservedAt: seen, Stale: !agent.Reachable,
				AgeSeconds: observationAge(now, seen), Summary: summary,
			})
		case doctor.TransportReporter, doctor.TransportDelegated:
			node := expected.NodeID
			if component.Transport == doctor.TransportDelegated {
				node = ""
			}
			row, ok := latest[expected.ServiceName+"\x00"+node]
			if !ok {
				out.MissingObservations = append(out.MissingObservations, missing)
				continue
			}
			status, summary := "UNKNOWN", "reporter carries no readiness"
			switch row.Readiness {
			case monmetrics.ReadinessReady:
				status, summary = "OK", "reported ready"
			case monmetrics.ReadinessNotReady:
				status, summary = "DOWN", "reported not ready: "+row.NotReadyReason
			}
			age := observationAge(now, row.LastSeenAt)
			out.HealthObservations = append(out.HealthObservations, Observation{
				Kind: "health", ComponentID: expected.ComponentID, ServiceName: expected.ServiceName, InstanceID: row.InstanceID,
				NodeID: expected.NodeID, BootID: row.BootID, Status: status, ObservedAt: row.LastSeenAt, Stale: age > 60,
				AgeSeconds: age, IntervalSeconds: 30, Summary: summary,
			})
		}
	}
	return nil
}

// latestAgent 取这台主机最近一次主机快照。
func latestAgent(agents []hostmetrics.AgentView, hostID string) (hostmetrics.AgentView, bool) {
	var found hostmetrics.AgentView
	ok := false
	for _, agent := range agents {
		if agent.HostID != hostID || (ok && parseTimestamp(agent.LastSeenAt).Before(parseTimestamp(found.LastSeenAt))) {
			continue
		}
		found, ok = agent, true
	}
	return found, ok
}

func (b Builder) addMetrics(ctx context.Context, components []doctor.Component, nodeID string, healthCheckIDs []string, now time.Time, out *Context) error {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil
	}
	serviceNames := make([]string, 0, len(components))
	for _, component := range components {
		serviceNames = append(serviceNames, component.ServiceName)
	}
	services, err := b.Metrics.Catalog().ListServicesForAt(ctx, serviceNames, nodeID, MaxObservations, now)
	if err != nil {
		return err
	}
	componentByService := componentMap(components)
	active := map[string][]monmetrics.MetricService{}
	for _, service := range services {
		component, ok := componentByService[service.ServiceName]
		if !ok || (nodeID != "" && service.NodeID != nodeID) {
			continue
		}
		key := service.ServiceName + "\x00" + service.NodeID
		active[key] = append(active[key], service)
		age := observationAge(now, service.LastSeenAt)
		status := "FRESH"
		if service.LastSeenAt.IsZero() || age > 120 {
			status = "FAIL"
		} else if age > 60 {
			status = "WARN"
		}
		out.ReporterObservations = append(out.ReporterObservations, Observation{Kind: "reporter", ComponentID: component.ComponentID, ServiceName: service.ServiceName, InstanceID: service.InstanceID, NodeID: service.NodeID, BootID: service.BootID, Status: status, ObservedAt: service.LastSeenAt, Stale: status != "FRESH", AgeSeconds: age, IntervalSeconds: 30, Summary: "latest Reporter snapshot"})
	}
	for _, rows := range active {
		serviceName, hostID := rows[0].ServiceName, rows[0].NodeID
		fresh := 0
		for _, row := range rows {
			if observationAge(now, row.LastSeenAt) <= 60 {
				fresh++
			}
		}
		if fresh > 1 {
			component := componentByService[serviceName]
			markReporterConflict(out, component.ComponentID, hostID)
			out.MissingObservations = append(out.MissingObservations, Observation{Kind: "identity", ComponentID: component.ComponentID, ServiceName: serviceName, NodeID: hostID, Status: "CONFLICT", Conflict: true, Summary: "multiple fresh Reporter identities"})
		}
	}
	for _, expected := range out.ExpectedComponents {
		component := componentByService[expected.ServiceName]
		if !expected.Expected || expected.Transport != string(doctor.TransportReporter) {
			continue
		}
		rows := active[expected.ServiceName+"\x00"+expected.NodeID]
		if len(rows) == 0 {
			age := observationAge(now, expected.DeploymentCreatedAt)
			status := "WARN"
			if expected.DeploymentCreatedAt.IsZero() || age > 120 {
				status = "FAIL"
			}
			out.MissingObservations = append(out.MissingObservations, Observation{Kind: "reporter", ComponentID: expected.ComponentID, ServiceName: expected.ServiceName, NodeID: expected.NodeID, Status: status, Stale: true, AgeSeconds: age, IntervalSeconds: 30, Summary: "Reporter observation is missing"})
			continue
		}
		if component.FunctionalObservability == doctor.FunctionalObservabilityDeferred {
			continue
		}
		for _, row := range rows {
			wantInstance := row.ServiceName + "@" + row.NodeID
			if row.NodeID == "" || row.BootID == "" || row.InstanceID != wantInstance {
				markReporterConflict(out, component.ComponentID, row.NodeID)
				out.MissingObservations = append(out.MissingObservations, Observation{Kind: "identity", ComponentID: component.ComponentID, ServiceName: row.ServiceName, InstanceID: row.InstanceID, NodeID: row.NodeID, BootID: row.BootID, Status: "CONFLICT", Conflict: true, Summary: "Reporter identity does not match the canonical service@node contract"})
			}
		}
	}
	selectedHealthChecks := stringSelection(healthCheckIDs)
	for _, component := range components {
		if component.FunctionalObservability != doctor.FunctionalObservabilityActive {
			continue
		}
		module := componentModule(component.ComponentID)
		lastSuccessMetric := report.ModuleMetricName(module, report.ModuleMetricLastSuccess)
		lastErrorMetric := report.ModuleMetricName(module, report.ModuleMetricLastError)
		businessWatermarkMetric := report.ModuleMetricName(module, report.ModuleMetricBusinessWatermark)
		inputWatermarkMetric := report.ModuleMetricName(module, report.ModuleMetricInputWatermark)
		runsMetric := report.ModuleMetricName(module, report.ModuleMetricRuns)
		metricErrorsMetric := report.ModuleMetricName(module, report.ModuleMetricErrors)
		lastMetricErrorMetric := report.ModuleMetricName(module, report.ModuleMetricLastMetricsError)
		metricNames := []string{
			runsMetric, lastSuccessMetric, lastErrorMetric, businessWatermarkMetric,
			inputWatermarkMetric, metricErrorsMetric, lastMetricErrorMetric,
		}
		instanceID := ""
		if nodeID != "" {
			instanceID = component.ServiceName + "@" + nodeID
		}
		series, err := b.Metrics.Catalog().FindSeriesByMetricNamesAt(
			ctx,
			component.ServiceName,
			instanceID,
			metricNames,
			MaxSeries+1,
			now,
		)
		if err != nil {
			return err
		}
		if len(series) > MaxSeries {
			return fmt.Errorf("Doctor metric series exceeds limit %d", MaxSeries)
		}
		for _, item := range series {
			labels := map[string]string{}
			if json.Unmarshal([]byte(item.LabelsJSON), &labels) != nil {
				continue
			}
			healthCheck := labels["health_check"]
			if item.MetricName != metricErrorsMetric && item.MetricName != lastMetricErrorMetric && len(selectedHealthChecks) > 0 && !selectedHealthChecks[healthCheck] {
				continue
			}
			latest, err := b.Metrics.Latest(ctx, item.SeriesID)
			if err != nil {
				continue
			}
			age := observationAge(now, latest.ObservedAt)
			observation := Observation{Kind: "module", ComponentID: component.ComponentID, ServiceName: component.ServiceName, InstanceID: latest.InstanceID, Status: map[bool]string{true: "STALE", false: "FRESH"}[item.IsStale], ObservedAt: latest.ObservedAt, Stale: item.IsStale, Value: latest.Value, AgeSeconds: age, IntervalSeconds: latest.IntervalSeconds, Summary: item.MetricName, DetailsJSON: item.LabelsJSON}
			out.ModuleObservations = append(out.ModuleObservations, observation)
			if item.MetricName == businessWatermarkMetric {
				out.Watermarks = append(out.Watermarks, Watermark{Module: module, Stage: labels["stage"], HealthCheckID: healthCheck, Value: latest.Value, ObservedAt: latest.ObservedAt, Status: observation.Status})
			}
		}
	}
	return nil
}

func (b Builder) addHosts(ctx context.Context, nodeID string, now time.Time, out *Context) error {
	if b.Hosts == nil {
		return nil
	}
	hosts, err := b.Hosts.ListAgents(ctx)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		if nodeID != "" && host.Hostname != nodeID && host.AgentID != nodeID {
			continue
		}
		out.Hosts = append(out.Hosts, host)
		history, err := b.Hosts.History(ctx, host.AgentID, now.Add(-hostmetrics.SampleRetention), now, hostmetrics.ForecastHistoryLimit)
		if err != nil {
			return err
		}
		out.Forecasts[host.AgentID] = hostmetrics.ForecastDisks(history, now)
		if len(out.Forecasts[host.AgentID]) == 0 {
			out.MissingObservations = append(out.MissingObservations, Observation{Kind: "disk_forecast", NodeID: host.Hostname, InstanceID: host.AgentID, Status: "UNKNOWN", Summary: "insufficient disk history"})
		}
	}
	return nil
}

func selectComponents(all []doctor.Component, selected []string) ([]doctor.Component, error) {
	if len(selected) == 0 {
		return append([]doctor.Component(nil), all...), nil
	}
	wanted := stringSelection(selected)
	known := map[string]bool{}
	out := make([]doctor.Component, 0, len(selected))
	for _, component := range all {
		known[component.ComponentID] = true
		if wanted[component.ComponentID] {
			out = append(out, component)
		}
	}
	for id := range wanted {
		if !known[id] {
			return nil, fmt.Errorf("unknown component_id %q", id)
		}
	}
	return out, nil
}

func validateHealthChecks(config []report.ModuleHealthCheck, selected []string) error {
	known := map[string]bool{}
	for _, healthCheck := range config {
		known[healthCheck.ID] = true
	}
	for _, id := range selected {
		if !known[id] {
			return fmt.Errorf("unknown health_check_id %q", id)
		}
	}
	return nil
}

func componentMap(components []doctor.Component) map[string]doctor.Component {
	out := make(map[string]doctor.Component, len(components))
	for _, component := range components {
		out[component.ServiceName] = component
	}
	return out
}

func stringSelection(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func parseTimestamp(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return parsed
}

func observationAge(now, observedAt time.Time) int64 {
	if observedAt.IsZero() {
		return 1<<63 - 1
	}
	if observedAt.After(now) {
		return 0
	}
	return int64(now.Sub(observedAt) / time.Second)
}

func markReporterConflict(out *Context, componentID, hostID string) {
	for i := range out.ReporterObservations {
		observation := &out.ReporterObservations[i]
		if observation.ComponentID == componentID && observation.NodeID == hostID {
			observation.Status = "CONFLICT"
			observation.Conflict = true
			observation.Summary = "Reporter identity conflict"
		}
	}
}

func enforceBounds(context Context) error {
	for name, count := range map[string]int{
		"expected_components": len(context.ExpectedComponents), "health_observations": len(context.HealthObservations),
		"reporter_observations": len(context.ReporterObservations), "module_observations": len(context.ModuleObservations),
		"watermarks": len(context.Watermarks), "host_resources": len(context.Hosts), "missing_observations": len(context.MissingObservations),
	} {
		limit := MaxObservations
		if name == "expected_components" {
			limit = doctor.MaxManifestComponents
		}
		if count > limit {
			return fmt.Errorf("%s exceeds limit %d", name, limit)
		}
	}
	if len(context.Alerts) > MaxAlerts {
		return fmt.Errorf("active_alerts exceeds limit %d", MaxAlerts)
	}
	sort.Slice(context.Watermarks, func(i, j int) bool {
		return context.Watermarks[i].Module+context.Watermarks[i].HealthCheckID <
			context.Watermarks[j].Module+context.Watermarks[j].HealthCheckID
	})
	return nil
}

// componentModule maps a component id to the module name used in module
// metrics; factor-mgr reports its metrics as module "factor".
func componentModule(componentID string) string {
	if componentID == "factor-mgr" {
		return "factor"
	}
	return componentID
}
