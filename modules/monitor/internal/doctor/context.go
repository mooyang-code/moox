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
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

const (
	MaxObservations = 128
	MaxAlerts       = 100
	MaxSeries       = 256
)

type DeploymentSource interface {
	DesiredDeployments(context.Context) ([]*adminpb.ServiceDeployment, error)
}

type Builder struct {
	Deployments DeploymentSource
	Checks      *store.CheckRepository
	Results     *store.ResultRepository
	Alerts      *store.AlertRepository
	Metrics     *monmetrics.QueryService
	Hosts       *hostmetrics.Store
	Pipelines   report.PipelineConfig
	Now         func() time.Time
}

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
	Module, Stage, Pipeline, Status string
	ObservedAt                      time.Time
	Value                           float64
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

func (b Builder) Build(ctx context.Context, nodeID string, componentIDs, pipelineIDs []string) (Context, error) {
	manifest, err := doctor.LoadEmbeddedManifest()
	if err != nil {
		return Context{}, err
	}
	components, err := selectComponents(manifest.Components, componentIDs)
	if err != nil {
		return Context{}, err
	}
	if err := validatePipelines(b.Pipelines, pipelineIDs); err != nil {
		return Context{}, err
	}
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	out := Context{GeneratedAt: now.Format(time.RFC3339Nano), ManifestChecksum: manifest.Checksum, Forecasts: map[string][]hostmetrics.DiskForecast{}}
	deployments, deploymentErr := b.loadDeployments(ctx)
	byService := make(map[string]*adminpb.ServiceDeployment, len(deployments))
	for _, deployment := range deployments {
		if deployment != nil && (nodeID == "" || deployment.GetNodeId() == nodeID) {
			byService[deployment.GetServiceName()] = deployment
		}
	}
	for _, component := range components {
		deployment := byService[component.ServiceName]
		expected := component.RequiredInDefaultProfile
		status, foundNode, healthURL := "missing", nodeID, ""
		if deployment != nil {
			status, foundNode = deployment.GetStatus(), deployment.GetNodeId()
			expected = deployment.GetStatus() == "active"
			healthURL = deploymentHealthURL(deployment)
		}
		out.ExpectedComponents = append(out.ExpectedComponents, ExpectedComponent{
			ComponentID: component.ComponentID, ServiceName: component.ServiceName, NodeID: foundNode,
			Expected: expected, DeploymentStatus: status, Transport: string(component.Transport),
			FunctionalObservability: string(component.FunctionalObservability), HealthURL: healthURL,
			DeploymentCreatedAt: parseTimestamp(deployment.GetCreatedAt()),
		})
	}
	if deploymentErr != nil {
		out.MissingObservations = append(out.MissingObservations, Observation{Kind: "sysdeploy", Status: "UNKNOWN", Summary: "SysDeploy facts unavailable"})
	}
	if err := b.addHealth(ctx, components, now, &out); err != nil {
		return Context{}, err
	}
	if err := b.addMetrics(ctx, components, nodeID, pipelineIDs, now, &out); err != nil {
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

func (b Builder) loadDeployments(ctx context.Context) ([]*adminpb.ServiceDeployment, error) {
	if b.Deployments == nil {
		return nil, fmt.Errorf("sysdeploy source is unavailable")
	}
	return b.Deployments.DesiredDeployments(ctx)
}

func (b Builder) addHealth(ctx context.Context, components []doctor.Component, now time.Time, out *Context) error {
	if b.Results == nil || b.Checks == nil {
		return nil
	}
	componentByService := componentMap(components)
	for _, expected := range out.ExpectedComponents {
		if !expected.Expected {
			continue
		}
		check, err := b.Checks.Get(ctx, "", expected.ServiceName)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			out.MissingObservations = append(out.MissingObservations, Observation{Kind: "health", ComponentID: expected.ComponentID, ServiceName: expected.ServiceName, NodeID: expected.NodeID, Status: "MISSING", Summary: "health check is not registered"})
			continue
		}
		if err != nil {
			return err
		}
		results, err := b.Results.Recent(ctx, "", expected.ServiceName, 3)
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
			Service            string `json:"service"`
			InstanceID         string `json:"instance_id"`
			NodeID             string `json:"node_id"`
			BootID             string `json:"boot_id"`
			PipelineConfigHash string `json:"pipeline_config_hash"`
		}
		component := componentByService[expected.ServiceName]
		if json.Unmarshal([]byte(latest.BodyExcerpt), &identity) == nil {
			observation.InstanceID, observation.NodeID, observation.BootID = identity.InstanceID, identity.NodeID, identity.BootID
			wantInstance := expected.ServiceName + "@" + expected.NodeID
			if component.Transport == doctor.TransportReporter && component.FunctionalObservability != doctor.FunctionalObservabilityDeferred && component.FunctionalObservability != doctor.FunctionalObservabilityNotApplicable &&
				(identity.Service != expected.ServiceName || identity.InstanceID != wantInstance || identity.NodeID != expected.NodeID || identity.BootID == "" || (b.Pipelines.Checksum != "" && identity.PipelineConfigHash != b.Pipelines.Checksum)) {
				observation.Status, observation.Conflict, observation.Summary = "CONFLICT", true, "health identity does not match the deployment contract"
			}
		} else if component.Transport == doctor.TransportReporter && component.FunctionalObservability != doctor.FunctionalObservabilityDeferred && component.FunctionalObservability != doctor.FunctionalObservabilityNotApplicable {
			observation.Status, observation.Conflict, observation.Summary = "CONFLICT", true, "health identity payload is missing or invalid"
		}
		out.HealthObservations = append(out.HealthObservations, observation)
	}
	return nil
}

func (b Builder) addMetrics(ctx context.Context, components []doctor.Component, nodeID string, pipelineIDs []string, now time.Time, out *Context) error {
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
			component := componentByService[serviceName]
			markReporterConflict(out, component.ComponentID)
			out.MissingObservations = append(out.MissingObservations, Observation{Kind: "identity", ComponentID: component.ComponentID, ServiceName: serviceName, NodeID: nodeID, Status: "CONFLICT", Conflict: true, Summary: "multiple fresh Reporter identities"})
		}
	}
	for _, expected := range out.ExpectedComponents {
		component := componentByService[expected.ServiceName]
		if !expected.Expected || expected.Transport != string(doctor.TransportReporter) {
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
		if component.FunctionalObservability == doctor.FunctionalObservabilityDeferred {
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
	selectedPipelines := stringSelection(pipelineIDs)
	for _, component := range components {
		if component.FunctionalObservability != doctor.FunctionalObservabilityActive {
			continue
		}
		series, err := b.Metrics.Catalog().FindSeriesAt(ctx, "", component.ServiceName, "", "", MaxSeries+1, now)
		if err != nil {
			return err
		}
		if len(series) > MaxSeries {
			return fmt.Errorf("Doctor metric series exceeds limit %d", MaxSeries)
		}
		canonicalWatermarks := map[string]bool{}
		for _, item := range series {
			if item.MetricName != "moox_business_watermark_timestamp_seconds" {
				continue
			}
			labels := map[string]string{}
			if json.Unmarshal([]byte(item.LabelsJSON), &labels) == nil {
				canonicalWatermarks[watermarkKey(labels)] = true
			}
		}
		for _, item := range series {
			if !strings.HasPrefix(item.MetricName, "moox_module_") && item.MetricName != "moox_business_watermark_timestamp_seconds" {
				continue
			}
			labels := map[string]string{}
			if json.Unmarshal([]byte(item.LabelsJSON), &labels) != nil {
				continue
			}
			if item.MetricName == "moox_module_watermark_timestamp_seconds" && canonicalWatermarks[watermarkKey(labels)] {
				continue
			}
			pipeline := labels["pipeline"]
			if item.MetricName != "moox_module_metrics_errors_total" && item.MetricName != "moox_module_metrics_last_error_timestamp_seconds" && len(selectedPipelines) > 0 && !selectedPipelines[pipeline] {
				continue
			}
			latest, err := b.Metrics.Latest(ctx, item.SeriesID)
			if err != nil {
				continue
			}
			age := observationAge(now, latest.ObservedAt)
			observation := Observation{Kind: "module", ComponentID: component.ComponentID, ServiceName: component.ServiceName, InstanceID: latest.InstanceID, Status: map[bool]string{true: "STALE", false: "FRESH"}[item.IsStale], ObservedAt: latest.ObservedAt, Stale: item.IsStale, Value: latest.Value, AgeSeconds: age, IntervalSeconds: latest.IntervalSeconds, Summary: item.MetricName, DetailsJSON: item.LabelsJSON}
			out.ModuleObservations = append(out.ModuleObservations, observation)
			if item.MetricName == "moox_business_watermark_timestamp_seconds" || item.MetricName == "moox_module_watermark_timestamp_seconds" {
				out.Watermarks = append(out.Watermarks, Watermark{Module: labels["module"], Stage: labels["stage"], Pipeline: pipeline, Value: latest.Value, ObservedAt: latest.ObservedAt, Status: observation.Status})
			}
		}
	}
	return nil
}

func watermarkKey(labels map[string]string) string {
	return labels["module"] + "\x00" + labels["stage"] + "\x00" + labels["pipeline"]
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

func validatePipelines(config report.PipelineConfig, selected []string) error {
	known := map[string]bool{}
	for _, pipeline := range config.Pipelines {
		known[pipeline.ID] = true
	}
	for _, id := range selected {
		if !known[id] {
			return fmt.Errorf("unknown pipeline_id %q", id)
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

func deploymentHealthURL(deployment *adminpb.ServiceDeployment) string {
	var extra struct {
		HealthURL string `json:"health_url"`
	}
	_ = json.Unmarshal([]byte(deployment.GetExtraConfig()), &extra)
	return extra.HealthURL
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
		return context.Watermarks[i].Module+context.Watermarks[i].Pipeline < context.Watermarks[j].Module+context.Watermarks[j].Pipeline
	})
	return nil
}
