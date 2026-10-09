// Package doctor assembles bounded Monitor facts for the interactive Doctor
// CLI. It does not execute checks or infer root causes.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/placement"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

const (
	MaxObservations = 128
	MaxAlerts       = 100
	MaxSeries       = 256
)

type Builder struct {
	// Placements 读取 SysDeploy 的主机与部署。
	Placements          placement.Source
	Checks              *store.CheckRepository
	Results             *store.ResultRepository
	Alerts              *store.AlertRepository
	Metrics             *monmetrics.QueryService
	Hosts               *hostmetrics.Store
	HealthChecks        []report.ModuleHealthCheck
	DatasetHealthPolicy report.DatasetHealthPolicy
	Now                 func() time.Time
}

// ExpectedComponent 是一个组件在一台主机上的部署。ServiceName 与 ComponentID 相同（上报指标的服务名就是组件 ID），
// NodeID 是主机 ID；DeploymentStatus 为 enabled、disabled 或 missing。
type ExpectedComponent struct {
	ComponentID, ServiceName, NodeID, DeploymentStatus, Transport, FunctionalObservability, HealthURL string
	Expected                                                                                          bool
	DeploymentCreatedAt                                                                               time.Time
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
	hosts, placements, deploymentErr := b.loadPlacements(ctx)
	out.ExpectedComponents = expectedComponents(components, hosts, placements, nodeID)
	if deploymentErr != nil {
		out.MissingObservations = append(out.MissingObservations, Observation{Kind: "placement", Status: "UNKNOWN", Summary: "SysDeploy placements unavailable"})
	}
	if err := b.addHealth(ctx, components, now, &out); err != nil {
		return Context{}, err
	}
	if err := b.addMetrics(ctx, components, nodeID, healthCheckIDs, now, &out); err != nil {
		return Context{}, err
	}
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

func (b Builder) loadPlacements(ctx context.Context) ([]*adminpb.DeployHost, []*adminpb.DeployPlacement, error) {
	if b.Placements == nil {
		return nil, nil, fmt.Errorf("SysDeploy 来源未配置")
	}
	hosts, err := b.Placements.Hosts(ctx)
	if err != nil {
		return nil, nil, err
	}
	placements, err := b.Placements.Placements(ctx)
	if err != nil {
		return nil, nil, err
	}
	return hosts, placements, nil
}

// expectedComponents 列出组件的部署。指定主机时每个组件一条，没有部署记为 missing；不指定主机时每条部署一条，
// 没有任何部署的组件记一条 missing。部署和所在主机都启用时才算「应当在运行」。
func expectedComponents(components []doctor.Component, hosts []*adminpb.DeployHost, placements []*adminpb.DeployPlacement, nodeID string) []ExpectedComponent {
	catalog := servicecatalog.Default()
	hostByID := make(map[string]*adminpb.DeployHost, len(hosts))
	for _, host := range hosts {
		if host != nil {
			hostByID[host.GetHostId()] = host
		}
	}
	out := make([]ExpectedComponent, 0, len(components))
	for _, component := range components {
		base := ExpectedComponent{
			ComponentID: component.ComponentID, ServiceName: component.ComponentID, NodeID: nodeID,
			DeploymentStatus: "missing", Transport: string(component.Transport), FunctionalObservability: string(component.Functional),
		}
		found := false
		for _, row := range placements {
			if row == nil || row.GetComponentId() != component.ComponentID || (nodeID != "" && row.GetHostId() != nodeID) {
				continue
			}
			found = true
			item := base
			host := hostByID[row.GetHostId()]
			item.NodeID, item.DeploymentStatus = row.GetHostId(), row.GetStatus()
			item.Expected = row.GetStatus() == placement.StatusEnabled && host != nil && host.GetStatus() == placement.StatusEnabled
			item.DeploymentCreatedAt = parseTimestamp(row.GetCreatedAt())
			if catalogComponent, ok := catalog.Component(component.ComponentID); ok && host != nil {
				item.HealthURL, _ = placement.HealthURL(host, *catalogComponent)
			}
			out = append(out, item)
		}
		if !found {
			out = append(out, base)
		}
	}
	return out
}

func (b Builder) addHealth(ctx context.Context, components []doctor.Component, now time.Time, out *Context) error {
	if b.Results == nil || b.Checks == nil {
		return nil
	}
	componentByID := componentMap(components)
	for _, expected := range out.ExpectedComponents {
		if !expected.Expected {
			continue
		}
		checkID := placement.CheckID(expected.NodeID, expected.ComponentID)
		check, err := b.Checks.Get(ctx, "", checkID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			out.MissingObservations = append(out.MissingObservations, Observation{Kind: "health", ComponentID: expected.ComponentID, ServiceName: expected.ServiceName, NodeID: expected.NodeID, Status: "MISSING", Summary: "health check is not registered"})
			continue
		}
		if err != nil {
			return err
		}
		results, err := b.Results.Recent(ctx, "", checkID, 3)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			out.MissingObservations = append(out.MissingObservations, Observation{Kind: "health", ComponentID: expected.ComponentID, ServiceName: expected.ServiceName, NodeID: expected.NodeID, Status: "MISSING", Summary: "health observation is missing"})
			continue
		}
		latest := results[0]
		interval := max(check.IntervalSeconds, 1)
		age := observationAge(now, latest.CheckedAt)
		status := strings.ToUpper(latest.Status)
		if !latest.Success {
			status = "DEGRADED"
		}
		if len(results) == 3 && !results[0].Success && !results[1].Success && !results[2].Success {
			status = "DOWN"
		}
		observation := Observation{Kind: "health", ComponentID: expected.ComponentID, ServiceName: expected.ServiceName, NodeID: expected.NodeID, Status: status, ObservedAt: latest.CheckedAt, Stale: age > int64(2*interval), AgeSeconds: age, IntervalSeconds: interval, Summary: healthSummary(latest)}
		var identity struct {
			Service                 string `json:"service"`
			InstanceID              string `json:"instance_id"`
			NodeID                  string `json:"node_id"`
			BootID                  string `json:"boot_id"`
			DatasetHealthPolicyHash string `json:"dataset_health_policy_hash"`
		}
		component := componentByID[expected.ComponentID]
		if json.Unmarshal([]byte(latest.BodyExcerpt), &identity) == nil {
			observation.InstanceID, observation.NodeID, observation.BootID = identity.InstanceID, identity.NodeID, identity.BootID
			wantInstance := expected.ServiceName + "@" + expected.NodeID
			identityMismatch := identity.Service != expected.ServiceName ||
				identity.InstanceID != wantInstance ||
				identity.NodeID != expected.NodeID ||
				identity.BootID == ""
			policyMismatch := expected.ComponentID == "monitor" &&
				b.DatasetHealthPolicy.Checksum != "" &&
				identity.DatasetHealthPolicyHash != b.DatasetHealthPolicy.Checksum
			if component.Transport == servicecatalog.TransportReporter &&
				component.Functional == servicecatalog.FunctionalActive &&
				(identityMismatch || policyMismatch) {
				observation.Status, observation.Conflict, observation.Summary = "CONFLICT", true, "health identity does not match the deployment contract"
			}
		} else if component.Transport == servicecatalog.TransportReporter && component.Functional == servicecatalog.FunctionalActive {
			observation.Status, observation.Conflict, observation.Summary = "CONFLICT", true, "health identity payload is missing or invalid"
		}
		out.HealthObservations = append(out.HealthObservations, observation)
	}
	return nil
}

func (b Builder) addMetrics(ctx context.Context, components []doctor.Component, nodeID string, healthCheckIDs []string, now time.Time, out *Context) error {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil
	}
	serviceNames := make([]string, 0, len(components))
	for _, component := range components {
		serviceNames = append(serviceNames, component.ComponentID)
	}
	services, err := b.Metrics.Catalog().ListServicesForAt(ctx, serviceNames, nodeID, MaxObservations, now)
	if err != nil {
		return err
	}
	componentByID := componentMap(components)
	active := map[string][]monmetrics.MetricService{}
	for _, service := range services {
		component, ok := componentByID[service.ServiceName]
		if !ok || (nodeID != "" && service.NodeID != nodeID) {
			continue
		}
		active[service.ServiceName] = append(active[service.ServiceName], service)
		age := observationAge(now, service.LastSeenAt)
		status := "FRESH"
		if service.LastSeenAt.IsZero() || age > 120 {
			status = "FAIL"
		} else if age > 60 {
			status = "WARN"
		}
		out.ReporterObservations = append(out.ReporterObservations, Observation{Kind: "reporter", ComponentID: component.ComponentID, ServiceName: service.ServiceName, InstanceID: service.InstanceID, NodeID: service.NodeID, BootID: service.BootID, Status: status, ObservedAt: service.LastSeenAt, Stale: status != "FRESH", AgeSeconds: age, IntervalSeconds: 30, Summary: "latest Reporter snapshot"})
	}
	for serviceName, rows := range active {
		fresh := 0
		for _, row := range rows {
			if observationAge(now, row.LastSeenAt) <= 60 {
				fresh++
			}
		}
		if fresh > 1 {
			component := componentByID[serviceName]
			markReporterConflict(out, component.ComponentID)
			out.MissingObservations = append(out.MissingObservations, Observation{Kind: "identity", ComponentID: component.ComponentID, ServiceName: serviceName, NodeID: nodeID, Status: "CONFLICT", Conflict: true, Summary: "multiple fresh Reporter identities"})
		}
	}
	for _, expected := range out.ExpectedComponents {
		component := componentByID[expected.ComponentID]
		if !expected.Expected || expected.Transport != string(servicecatalog.TransportReporter) {
			continue
		}
		rows := active[expected.ServiceName]
		if len(rows) == 0 {
			age := observationAge(now, expected.DeploymentCreatedAt)
			status := "WARN"
			if expected.DeploymentCreatedAt.IsZero() || age > 120 {
				status = "FAIL"
			}
			out.MissingObservations = append(out.MissingObservations, Observation{Kind: "reporter", ComponentID: expected.ComponentID, ServiceName: expected.ServiceName, NodeID: expected.NodeID, Status: status, Stale: true, AgeSeconds: age, IntervalSeconds: 30, Summary: "Reporter observation is missing"})
			continue
		}
		if component.Functional == servicecatalog.FunctionalDeferred {
			continue
		}
		for _, row := range rows {
			wantInstance := row.ServiceName + "@" + row.NodeID
			if row.NodeID == "" || row.BootID == "" || row.InstanceID != wantInstance {
				markReporterConflict(out, component.ComponentID)
				out.MissingObservations = append(out.MissingObservations, Observation{Kind: "identity", ComponentID: component.ComponentID, ServiceName: row.ServiceName, InstanceID: row.InstanceID, NodeID: row.NodeID, BootID: row.BootID, Status: "CONFLICT", Conflict: true, Summary: "Reporter identity does not match the canonical service@node contract"})
			}
		}
	}
	selectedHealthChecks := stringSelection(healthCheckIDs)
	for _, component := range components {
		if component.Functional != servicecatalog.FunctionalActive {
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
			instanceID = component.ComponentID + "@" + nodeID
		}
		series, err := b.Metrics.Catalog().FindSeriesByMetricNamesAt(
			ctx,
			component.ComponentID,
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
			observation := Observation{Kind: "module", ComponentID: component.ComponentID, ServiceName: component.ComponentID, InstanceID: latest.InstanceID, Status: map[bool]string{true: "STALE", false: "FRESH"}[item.IsStale], ObservedAt: latest.ObservedAt, Stale: item.IsStale, Value: latest.Value, AgeSeconds: age, IntervalSeconds: latest.IntervalSeconds, Summary: item.MetricName, DetailsJSON: item.LabelsJSON}
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
		history, err := b.Hosts.HistoryAt(ctx, host.AgentID, now.Add(-7*24*time.Hour), now, now, hostmetrics.ForecastHistoryLimit)
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
		out[component.ComponentID] = component
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

func healthSummary(result domain.CheckResult) string {
	if result.Success {
		return "service health check passed"
	}
	if result.ErrorMessage != "" {
		return result.ErrorMessage
	}
	return "service health check failed"
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

func markReporterConflict(out *Context, componentID string) {
	for i := range out.ReporterObservations {
		observation := &out.ReporterObservations[i]
		if observation.ComponentID == componentID {
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

// componentModule 是组件上报模块指标时使用的模块名：factor-mgr 的模块名是 factor，其余与组件 ID 相同。
func componentModule(componentID string) string {
	return strings.TrimSuffix(componentID, "-mgr")
}
