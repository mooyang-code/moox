package doctor

import (
	"context"
	"fmt"
	"strings"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
)

type ContextClient interface {
	GetDoctorContext(context.Context, *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error)
}

type DiagnoseOptions struct {
	NodeID    string
	CheckIDs  []string
	Client    ContextClient
	Prober    HTTPProber
	Pipelines report.PipelineConfig
	Now       func() time.Time
}

type diagnoseRunner struct {
	options DiagnoseOptions
	context *monitorpb.GetDoctorContextRsp
}

func RunDiagnose(ctx context.Context, options DiagnoseOptions) (core.Report, error) {
	manifest, err := core.LoadEmbeddedManifest()
	if err != nil {
		return core.Report{}, err
	}
	var snapshot *monitorpb.GetDoctorContextRsp
	var contextErr error
	if options.Client == nil {
		contextErr = fmt.Errorf("Monitor client is unavailable")
	} else {
		snapshot, contextErr = options.Client.GetDoctorContext(ctx, &monitorpb.GetDoctorContextReq{NodeId: options.NodeID, PipelineIds: pipelineIDs(options.Pipelines)})
	}
	if contextErr != nil {
		runner := core.RunnerFunc(func(_ context.Context, spec core.CheckSpec, _ []core.DependencyContext) core.CheckResult {
			return checkResult(spec.ID, core.StatusUnknown, "Monitor context is unavailable; run bootstrap locally", contextErr, "run_bootstrap")
		})
		report, runErr := (core.Engine{Mode: core.ModeDiagnose, Now: options.Now}).Run(ctx, []core.CheckSpec{{ID: "diagnose.context"}}, runner)
		if runErr != nil {
			return core.Report{}, runErr
		}
		report.RunID, report.ManifestChecksum = newRunID(), manifest.Checksum
		return report, nil
	}
	specs := diagnoseSpecs(snapshot, options.Pipelines)
	specs, err = selectSpecs(specs, options.CheckIDs)
	if err != nil {
		return core.Report{}, err
	}
	runner := &diagnoseRunner{options: options, context: snapshot}
	result, err := (core.Engine{Mode: core.ModeDiagnose, Now: options.Now}).Run(ctx, specs, core.RunnerFunc(runner.run))
	if err != nil {
		return core.Report{}, err
	}
	result.RunID = newRunID()
	result.ManifestChecksum = snapshot.GetManifestChecksum()
	for _, observation := range snapshot.GetMissingObservations() {
		result.MissingObservations = append(result.MissingObservations, core.Observation{Source: observation.GetKind(), ObservedAt: parseWireTime(observation.GetObservedAt()), Summary: observation.GetSummary()})
	}
	return result, nil
}

func diagnoseSpecs(snapshot *monitorpb.GetDoctorContextRsp, pipelines report.PipelineConfig) []core.CheckSpec {
	specs := []core.CheckSpec{{ID: "diagnose.context"}}
	health := map[string]string{}
	for _, component := range snapshot.GetExpectedComponents() {
		scope := component.GetComponentId() + "@" + component.GetNodeId()
		health[component.GetComponentId()] = "service.health:" + scope
		specs = append(specs, core.CheckSpec{ID: health[component.GetComponentId()], OptionalDependencies: []string{"diagnose.context"}})
	}
	deps := []string{}
	for _, id := range []string{"eventbus", "moox_monitor"} {
		if health[id] != "" {
			deps = append(deps, health[id])
		}
	}
	specs = append(specs, core.CheckSpec{ID: "monitor.metrics_delivery:" + firstNode(snapshot), RequiredDependencies: deps})
	for _, component := range snapshot.GetExpectedComponents() {
		reporterID := "monitor.reporter_coverage:" + component.GetComponentId() + "@" + component.GetNodeId()
		reporterDependencies := []string{"monitor.metrics_delivery:" + firstNode(snapshot)}
		if component.GetFunctionalObservability() == "deferred" {
			reporterDependencies = []string{"diagnose.context"}
		}
		specs = append(specs, core.CheckSpec{ID: reporterID, RequiredDependencies: reporterDependencies, OptionalDependencies: []string{health[component.GetComponentId()]}})
		if component.GetFunctionalObservability() != "not_applicable" {
			freshnessDependencies := []string{reporterID}
			if component.GetFunctionalObservability() == "deferred" {
				freshnessDependencies = []string{"diagnose.context"}
			}
			specs = append(specs, core.CheckSpec{ID: "module.freshness:" + component.GetComponentId() + "@" + component.GetNodeId(), RequiredDependencies: freshnessDependencies, OptionalDependencies: []string{health[component.GetComponentId()]}})
		}
	}
	for _, pipeline := range pipelines.Pipelines {
		if pipeline.Enabled {
			specs = append(specs, core.CheckSpec{ID: "module.pipeline_lag:" + pipeline.Module + ":" + pipeline.ID, RequiredDependencies: []string{"diagnose.context"}})
		}
	}
	specs = append(specs, core.CheckSpec{ID: "host.disk_forecast:" + firstNode(snapshot)})
	return specs
}

func (r *diagnoseRunner) run(ctx context.Context, spec core.CheckSpec, _ []core.DependencyContext) core.CheckResult {
	if spec.ID == "diagnose.context" {
		return checkResult(spec.ID, core.StatusPass, "bounded Monitor context loaded", nil)
	}
	if strings.HasPrefix(spec.ID, "service.health:") {
		component := r.expectedFromID(spec.ID)
		if component == nil || !component.GetExpected() {
			return checkResult(spec.ID, core.StatusSkipped, "component is disabled or not expected", nil)
		}
		observation := findObservation(r.context.GetHealthObservations(), component.GetComponentId())
		if observation != nil && !observation.GetStale() && !observation.GetConflict() {
			if observation.GetStatus() == "OK" {
				return checkResult(spec.ID, core.StatusPass, "Monitor health observation is current", nil)
			}
			return checkResult(spec.ID, core.StatusFail, "Monitor reports an unhealthy service", nil, "restart_service_manually")
		}
		if component.GetHealthUrl() == "" {
			return checkResult(spec.ID, core.StatusUnknown, "health observation is missing and no fixed endpoint is available", nil, "run_bootstrap")
		}
		probe, err := r.options.Prober.Get(ctx, component.GetHealthUrl())
		if err != nil {
			return checkResult(spec.ID, core.StatusFail, "direct health evidence confirms a service failure", err, "restart_service_manually")
		}
		result := checkResult(spec.ID, core.StatusPass, "missing or stale Monitor health fact was confirmed by a bounded direct read", nil)
		result.Observations = []core.Observation{{Source: "direct_health", ObservedAt: probe.ObservedAt, Summary: "signed direct health response", Digest: probe.Digest}}
		return result
	}
	if strings.HasPrefix(spec.ID, "monitor.metrics_delivery:") {
		missing := false
		for _, observation := range r.context.GetMissingObservations() {
			criticalComponent := observation.GetComponentId() == "eventbus" || observation.GetComponentId() == "moox_monitor"
			if criticalComponent && (observation.GetKind() == "reporter" || observation.GetKind() == "identity") {
				missing = true
			}
		}
		if missing {
			return checkResult(spec.ID, core.StatusFail, "business health is available but the metrics delivery chain has missing or conflicting facts", nil, "verify_eventbus_credentials")
		}
		return checkResult(spec.ID, core.StatusPass, "metrics delivery facts are present", nil)
	}
	if strings.HasPrefix(spec.ID, "monitor.reporter_coverage:") {
		component := r.expectedFromID(spec.ID)
		if component == nil || !component.GetExpected() {
			return checkResult(spec.ID, core.StatusSkipped, "component is disabled or not expected", nil)
		}
		if component.GetFunctionalObservability() == "deferred" {
			return checkResult(spec.ID, core.StatusSkipped, "storage_observability_deferred", nil)
		}
		if component.GetTransport() != "reporter" {
			return checkResult(spec.ID, core.StatusSkipped, "component does not use Reporter transport", nil)
		}
		observation := findObservation(r.context.GetReporterObservations(), component.GetComponentId())
		if observation == nil {
			return r.directMetrics(ctx, spec.ID, component)
		}
		if observation.GetConflict() {
			return checkResult(spec.ID, core.StatusFail, "Reporter identity conflict fails closed", nil, "verify_service_identity")
		}
		if observation.GetStale() {
			return r.directMetrics(ctx, spec.ID, component)
		}
		return checkResult(spec.ID, core.StatusPass, "Reporter observation is current", nil)
	}
	if strings.HasPrefix(spec.ID, "module.freshness:") {
		component := r.expectedFromID(spec.ID)
		if component == nil || !component.GetExpected() {
			return checkResult(spec.ID, core.StatusSkipped, "component is disabled or not expected", nil)
		}
		if component.GetFunctionalObservability() == "deferred" {
			return checkResult(spec.ID, core.StatusSkipped, "storage_observability_deferred", nil)
		}
		observation := findObservation(r.context.GetModuleObservations(), component.GetComponentId())
		if observation == nil {
			return checkResult(spec.ID, core.StatusUnknown, "no functional observation exists for an enabled workload", nil, "inspect_pipeline_input")
		}
		if observation.GetStale() {
			return checkResult(spec.ID, core.StatusFail, "functional observation is stale", nil, "inspect_pipeline_input")
		}
		return checkResult(spec.ID, core.StatusPass, "functional observation is current", nil)
	}
	if strings.HasPrefix(spec.ID, "module.pipeline_lag:") {
		pipeline := r.pipelineFromID(spec.ID)
		if pipeline == nil {
			return checkResult(spec.ID, core.StatusFail, "unknown pipeline", nil)
		}
		if pipeline.CrossesStorageDeferred {
			return checkResult(spec.ID, core.StatusSkipped, "storage_observability_deferred", nil)
		}
		for _, watermark := range r.context.GetWatermarks() {
			if watermark.GetPipeline() == pipeline.ID && watermark.GetModule() == pipeline.Module {
				if watermark.GetStatus() == "STALE" {
					return checkResult(spec.ID, core.StatusFail, "pipeline output watermark is stale", nil, "inspect_pipeline_input")
				}
				return checkResult(spec.ID, core.StatusPass, "pipeline output watermark is available", nil)
			}
		}
		return checkResult(spec.ID, core.StatusPass, "pipeline has no advancing input fact; treated as IDLE", nil)
	}
	if strings.HasPrefix(spec.ID, "host.disk_forecast:") {
		if len(r.context.GetDiskForecasts()) == 0 {
			return checkResult(spec.ID, core.StatusUnknown, "disk forecast has insufficient history", nil, "free_disk_space")
		}
		status := core.StatusPass
		for _, forecast := range r.context.GetDiskForecasts() {
			switch forecast.GetStatus() {
			case "FAIL":
				status = core.StatusFail
			case "WARN":
				if status != core.StatusFail {
					status = core.StatusWarn
				}
			case "UNKNOWN":
				if status == core.StatusPass {
					status = core.StatusUnknown
				}
			}
		}
		return checkResult(spec.ID, status, "disk forecasts evaluated with the seven-day median policy", nil, "free_disk_space")
	}
	return checkResult(spec.ID, core.StatusFail, "unknown diagnose check", nil)
}

func (r *diagnoseRunner) directMetrics(ctx context.Context, id string, component *monitorpb.DoctorExpectedComponent) core.CheckResult {
	if component.GetHealthUrl() == "" {
		return checkResult(id, core.StatusFail, "Reporter observation is missing and no fixed metrics endpoint is available", nil, "verify_eventbus_credentials")
	}
	metricsURL := strings.TrimSuffix(strings.TrimSuffix(component.GetHealthUrl(), "/readyz"), "/healthz") + "/metrics"
	probe, err := r.options.Prober.Get(ctx, metricsURL)
	if err != nil {
		return checkResult(id, core.StatusFail, "Reporter observation is missing and direct metrics read failed", err, "verify_eventbus_credentials")
	}
	result := checkResult(id, core.StatusWarn, "Reporter delivery is stale or missing while the business metrics endpoint remains healthy", nil, "verify_eventbus_credentials")
	result.Observations = []core.Observation{{Source: "direct_metrics", ObservedAt: probe.ObservedAt, Summary: "signed direct metrics response", Digest: probe.Digest}}
	return result
}

func (r *diagnoseRunner) expectedFromID(id string) *monitorpb.DoctorExpectedComponent {
	for _, component := range r.context.GetExpectedComponents() {
		if strings.Contains(id, component.GetComponentId()+"@") {
			return component
		}
	}
	return nil
}

func (r *diagnoseRunner) pipelineFromID(id string) *report.Pipeline {
	for i := range r.options.Pipelines.Pipelines {
		pipeline := &r.options.Pipelines.Pipelines[i]
		if strings.HasSuffix(id, ":"+pipeline.ID) {
			return pipeline
		}
	}
	return nil
}

func findObservation(items []*monitorpb.DoctorObservation, componentID string) *monitorpb.DoctorObservation {
	for _, item := range items {
		if item.GetComponentId() == componentID {
			return item
		}
	}
	return nil
}

func pipelineIDs(config report.PipelineConfig) []string {
	ids := make([]string, 0, len(config.Pipelines))
	for _, pipeline := range config.Pipelines {
		if pipeline.Enabled {
			ids = append(ids, pipeline.ID)
		}
	}
	return ids
}

func firstNode(snapshot *monitorpb.GetDoctorContextRsp) string {
	for _, component := range snapshot.GetExpectedComponents() {
		if component.GetNodeId() != "" {
			return component.GetNodeId()
		}
	}
	return "local"
}

func parseWireTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
