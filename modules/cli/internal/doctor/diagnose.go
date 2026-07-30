package doctor

import (
	"context"
	"encoding/json"
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
	NodeID       string
	CheckIDs     []string
	Client       ContextClient
	Prober       HTTPProber
	HealthChecks []report.ModuleHealthCheck
	Now          func() time.Time
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
		snapshot, contextErr = options.Client.GetDoctorContext(ctx, &monitorpb.GetDoctorContextReq{NodeId: options.NodeID, HealthCheckIds: healthCheckIDs(options.HealthChecks)})
		if contextErr == nil && snapshot == nil {
			contextErr = fmt.Errorf("Monitor returned an empty Doctor Context")
		}
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
	if snapshot.GetManifestChecksum() != manifest.Checksum {
		mismatch := fmt.Errorf("Monitor context manifest checksum %q does not match embedded checksum %q", snapshot.GetManifestChecksum(), manifest.Checksum)
		runner := core.RunnerFunc(func(_ context.Context, spec core.CheckSpec, _ []core.DependencyContext) core.CheckResult {
			return checkResult(spec.ID, core.StatusFail, "Monitor context provenance is invalid", mismatch, "run_bootstrap")
		})
		report, runErr := (core.Engine{Mode: core.ModeDiagnose, Now: options.Now}).Run(ctx, []core.CheckSpec{{ID: "diagnose.context"}}, runner)
		if runErr != nil {
			return core.Report{}, runErr
		}
		report.RunID, report.ManifestChecksum = newRunID(), manifest.Checksum
		return report, nil
	}
	specs := diagnoseSpecs(snapshot, options.HealthChecks)
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
	result.ManifestChecksum = manifest.Checksum
	for _, observation := range snapshot.GetMissingObservations() {
		result.MissingObservations = append(result.MissingObservations, core.Observation{Source: observation.GetKind(), ObservedAt: parseWireTime(observation.GetObservedAt()), Summary: observation.GetSummary()})
	}
	return result, nil
}

func diagnoseSpecs(snapshot *monitorpb.GetDoctorContextRsp, healthChecks []report.ModuleHealthCheck) []core.CheckSpec {
	specs := []core.CheckSpec{{ID: "diagnose.context"}}
	health := map[string]string{}
	freshnessByModule := map[string]string{}
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
		if !component.GetExpected() {
			continue
		}
		reporterID := "monitor.reporter_coverage:" + component.GetComponentId() + "@" + component.GetNodeId()
		reporterDependencies := []string{"monitor.metrics_delivery:" + firstNode(snapshot)}
		if component.GetFunctionalObservability() == "deferred" || component.GetFunctionalObservability() == "not_applicable" {
			reporterDependencies = []string{"diagnose.context"}
		}
		specs = append(specs, core.CheckSpec{ID: reporterID, RequiredDependencies: reporterDependencies, OptionalDependencies: []string{health[component.GetComponentId()]}})
		module := strings.TrimPrefix(component.GetComponentId(), "moox_")
		freshnessEnabled := component.GetFunctionalObservability() == "deferred" || moduleFreshnessEnabled(healthChecks, module)
		if component.GetFunctionalObservability() != "not_applicable" && freshnessEnabled {
			freshnessDependencies := []string{reporterID}
			if component.GetFunctionalObservability() == "deferred" {
				freshnessDependencies = []string{"diagnose.context"}
			}
			freshnessID := "module.freshness:" + component.GetComponentId() + "@" + component.GetNodeId()
			freshnessByModule[module] = freshnessID
			specs = append(specs, core.CheckSpec{ID: freshnessID, RequiredDependencies: freshnessDependencies, OptionalDependencies: []string{health[component.GetComponentId()]}})
		}
	}
	for _, healthCheck := range healthChecks {
		if healthCheck.Enabled && healthCheck.CheckWatermark {
			dependencies := []string{"diagnose.context"}
			if freshnessByModule[healthCheck.Module] != "" {
				dependencies = append(dependencies, freshnessByModule[healthCheck.Module])
			}
			specs = append(specs, core.CheckSpec{ID: "module.health_check:" + healthCheck.Module + ":" + healthCheck.ID, RequiredDependencies: dependencies})
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
		if observation != nil && observation.GetConflict() {
			return checkResult(spec.ID, core.StatusFail, "health identity conflict fails closed", nil, "verify_service_identity")
		}
		if observation != nil && !observation.GetStale() && !observation.GetConflict() {
			if observation.GetStatus() == "OK" {
				return checkResult(spec.ID, core.StatusPass, "Monitor health observation is current", nil)
			}
			if observation.GetStatus() == "DOWN" {
				return checkResult(spec.ID, core.StatusFail, "Monitor reports three consecutive health failures", nil, "restart_service_manually")
			}
			return checkResult(spec.ID, core.StatusWarn, "Monitor has fewer than three consecutive health failures", nil, "restart_service_manually")
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
		stale := false
		for _, observation := range r.context.GetReporterObservations() {
			criticalComponent := observation.GetComponentId() == "eventbus" || observation.GetComponentId() == "moox_monitor"
			if criticalComponent && (observation.GetStatus() == "FAIL" || observation.GetStale() || observation.GetConflict()) {
				stale = true
			}
		}
		for _, observation := range r.context.GetMissingObservations() {
			criticalComponent := observation.GetComponentId() == "eventbus" || observation.GetComponentId() == "moox_monitor"
			if criticalComponent && (observation.GetKind() == "reporter" || observation.GetKind() == "identity") {
				missing = true
			}
		}
		if missing || stale {
			return checkResult(spec.ID, core.StatusFail, "business health is available but the metrics delivery chain has missing or conflicting facts", nil, "verify_eventbus_credentials")
		}
		return checkResult(spec.ID, core.StatusPass, "metrics delivery facts are present", nil)
	}
	if strings.HasPrefix(spec.ID, "monitor.reporter_coverage:") {
		component := r.expectedFromID(spec.ID)
		if component == nil || !component.GetExpected() {
			return checkResult(spec.ID, core.StatusSkipped, "component is disabled or not expected", nil)
		}
		if component.GetFunctionalObservability() == "deferred" || component.GetFunctionalObservability() == "not_applicable" {
			if component.GetFunctionalObservability() == "not_applicable" {
				return checkResult(spec.ID, core.StatusSkipped, "functional_observability_not_applicable", nil)
			}
			return checkResult(spec.ID, core.StatusSkipped, "storage_observability_deferred", nil)
		}
		if component.GetTransport() != "reporter" {
			return checkResult(spec.ID, core.StatusSkipped, "component does not use Reporter transport", nil)
		}
		module := strings.TrimPrefix(component.GetComponentId(), "moox_")
		metricErrorsName := report.ModuleMetricName(module, report.ModuleMetricErrors)
		for _, metric := range r.context.GetModuleObservations() {
			if metric.GetComponentId() == component.GetComponentId() && metric.GetSummary() == metricErrorsName && metric.GetValue() > 0 && recentMetricError(r.context.GetModuleObservations(), component.GetComponentId(), r.now()) {
				return checkResult(spec.ID, core.StatusFail, "module metric observations have been rejected", nil, "inspect_health_check_input")
			}
		}
		observation := findObservation(r.context.GetReporterObservations(), component.GetComponentId())
		if observation == nil {
			severity := core.StatusWarn
			if missing := findObservationKind(r.context.GetMissingObservations(), component.GetComponentId(), "reporter"); missing != nil && missing.GetStatus() == "FAIL" {
				severity = core.StatusFail
			}
			return r.directMetrics(ctx, spec.ID, component, severity)
		}
		if observation.GetConflict() {
			return checkResult(spec.ID, core.StatusFail, "Reporter identity conflict fails closed", nil, "verify_service_identity")
		}
		if hasObservationConflict(r.context.GetMissingObservations(), component.GetComponentId()) {
			return checkResult(spec.ID, core.StatusFail, "Reporter identity conflict fails closed", nil, "verify_service_identity")
		}
		if observation.GetStale() {
			severity := core.StatusWarn
			if observation.GetStatus() == "FAIL" || (observation.GetIntervalSeconds() > 0 && observation.GetAgeSeconds() > int64(4*observation.GetIntervalSeconds())) {
				severity = core.StatusFail
			}
			return r.directMetrics(ctx, spec.ID, component, severity)
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
		observations := r.moduleSuccessObservations(component.GetComponentId())
		if len(observations) == 0 {
			return checkResult(spec.ID, core.StatusUnknown, "no functional observation exists for an enabled workload", nil, "inspect_health_check_input")
		}
		module := strings.TrimPrefix(component.GetComponentId(), "moox_")
		byHealthCheck := map[string]time.Time{}
		for _, observation := range observations {
			labels := map[string]string{}
			if json.Unmarshal([]byte(observation.GetDetailsJson()), &labels) != nil {
				continue
			}
			at := time.Unix(int64(observation.GetValue()), 0).UTC()
			if at.IsZero() {
				continue
			}
			byHealthCheck[labels["health_check"]] = at
		}
		if len(byHealthCheck) == 0 {
			return checkResult(spec.ID, core.StatusUnknown, "functional last-success timestamp is invalid", nil, "inspect_health_check_input")
		}
		if len(r.options.HealthChecks) > 0 {
			for _, healthCheck := range r.options.HealthChecks {
				if !healthCheck.Enabled || healthCheck.Module != module {
					continue
				}
				at, ok := byHealthCheck[healthCheck.ID]
				if !ok {
					return checkResult(spec.ID, core.StatusUnknown, "functional last-success timestamp is missing for an enabled health check", nil, "inspect_health_check_input")
				}
				threshold := r.moduleFreshnessThreshold(module)
				if healthCheck.MaxLag > threshold {
					threshold = healthCheck.MaxLag
				}
				if at.After(r.now().Add(time.Minute)) || r.now().Sub(at) > threshold {
					return checkResult(spec.ID, core.StatusFail, "functional last-success timestamp is stale", nil, "inspect_health_check_input")
				}
			}
			return checkResult(spec.ID, core.StatusPass, "functional last-success timestamps are current", nil)
		}
		freshest := time.Time{}
		for _, at := range byHealthCheck {
			if at.After(freshest) {
				freshest = at
			}
		}
		if freshest.After(r.now().Add(time.Minute)) || r.now().Sub(freshest) > r.moduleFreshnessThreshold(module) {
			return checkResult(spec.ID, core.StatusFail, "functional last-success timestamp is stale", nil, "inspect_health_check_input")
		}
		return checkResult(spec.ID, core.StatusPass, "functional last-success timestamp is current", nil)
	}
	if strings.HasPrefix(spec.ID, "module.health_check:") {
		healthCheck := r.healthCheckFromID(spec.ID)
		if healthCheck == nil {
			return checkResult(spec.ID, core.StatusFail, "unknown health check", nil)
		}
		if !r.moduleExpected(healthCheck.Module) {
			return checkResult(spec.ID, core.StatusSkipped, "no_enabled_workload", nil)
		}
		if healthCheck.ObservabilityDeferred {
			return checkResult(spec.ID, core.StatusSkipped, "storage_observability_deferred", nil)
		}
		var input time.Time
		var inputObservation *monitorpb.DoctorObservation
		inputMetric := report.ModuleMetricName(healthCheck.Module, report.ModuleMetricInputWatermark)
		for _, observation := range r.context.GetModuleObservations() {
			if observation.GetSummary() != inputMetric {
				continue
			}
			labels := map[string]string{}
			if json.Unmarshal([]byte(observation.GetDetailsJson()), &labels) == nil && labels["health_check"] == healthCheck.ID {
				input = time.Unix(int64(observation.GetValue()), 0).UTC()
				inputObservation = observation
				break
			}
		}
		if inputObservation != nil && inputObservation.GetStale() {
			return checkResult(spec.ID, core.StatusUnknown, "health check input observation is stale", nil, "inspect_health_check_input")
		}
		var output time.Time
		outputStatus := ""
		for _, watermark := range r.context.GetWatermarks() {
			if watermark.GetHealthCheckId() == healthCheck.ID && watermark.GetModule() == healthCheck.Module {
				output = time.Unix(int64(watermark.GetValue()), 0).UTC()
				outputStatus = watermark.GetStatus()
				break
			}
		}
		if inputObservation != nil && outputStatus == "STALE" {
			return checkResult(spec.ID, core.StatusFail, "health check output watermark is stale while input exists", nil, "inspect_health_check_input")
		}
		if input.After(r.now().Add(time.Minute)) || (!output.IsZero() && output.After(r.now().Add(time.Minute))) {
			return checkResult(spec.ID, core.StatusUnknown, "health check watermark is in the future", nil, "inspect_health_check_input")
		}
		// The Monitor snapshot has no prior input watermark. Never derive one
		// from the output watermark: doing so turns an old stalled output into
		// the evaluator's input-idle PASS path.
		verdict := report.EvaluateModuleHealth(report.ModuleHealthSignals{EnabledWorkloads: 1, InputWatermark: input, OutputWatermark: output, MaxLag: healthCheck.MaxLag, ObservabilityDeferred: healthCheck.ObservabilityDeferred}, r.now())
		return checkResult(spec.ID, coreStatus(verdict.Status), verdict.Reason, nil, "inspect_health_check_input")
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

func recentMetricError(observations []*monitorpb.DoctorObservation, componentID string, now time.Time) bool {
	module := strings.TrimPrefix(componentID, "moox_")
	metricName := report.ModuleMetricName(module, report.ModuleMetricLastMetricsError)
	for _, metric := range observations {
		if metric.GetComponentId() != componentID || metric.GetSummary() != metricName {
			continue
		}
		at := time.Unix(int64(metric.GetValue()), 0).UTC()
		return !at.IsZero() && now.Sub(at) <= 2*time.Minute
	}
	return false
}

func (r *diagnoseRunner) directMetrics(ctx context.Context, id string, component *monitorpb.DoctorExpectedComponent, severity core.CheckStatus) core.CheckResult {
	if component.GetHealthUrl() == "" {
		return checkResult(id, core.StatusFail, "Reporter observation is missing and no fixed metrics endpoint is available", nil, "verify_eventbus_credentials")
	}
	metricsURL := strings.TrimSuffix(strings.TrimSuffix(component.GetHealthUrl(), "/readyz"), "/healthz") + "/metrics"
	probe, err := r.options.Prober.Get(ctx, metricsURL)
	if err != nil {
		return checkResult(id, core.StatusFail, "Reporter observation is missing and direct metrics read failed", err, "verify_eventbus_credentials")
	}
	result := checkResult(id, severity, "Reporter delivery is stale or missing while the business metrics endpoint remains healthy", nil, "verify_eventbus_credentials")
	result.Observations = []core.Observation{{Source: "direct_metrics", ObservedAt: probe.ObservedAt, Summary: "signed direct metrics response", Digest: probe.Digest}}
	return result
}

func (r *diagnoseRunner) now() time.Time {
	if r.options.Now != nil {
		return r.options.Now().UTC()
	}
	return time.Now().UTC()
}

func hasObservationConflict(items []*monitorpb.DoctorObservation, componentID string) bool {
	for _, item := range items {
		if item.GetComponentId() == componentID && item.GetConflict() {
			return true
		}
	}
	return false
}

func coreStatus(status string) core.CheckStatus {
	switch status {
	case "PASS":
		return core.StatusPass
	case "WARN":
		return core.StatusWarn
	case "FAIL":
		return core.StatusFail
	case "SKIPPED":
		return core.StatusSkipped
	default:
		return core.StatusUnknown
	}
}

func (r *diagnoseRunner) expectedFromID(id string) *monitorpb.DoctorExpectedComponent {
	for _, component := range r.context.GetExpectedComponents() {
		if strings.Contains(id, component.GetComponentId()+"@") {
			return component
		}
	}
	return nil
}

func (r *diagnoseRunner) healthCheckFromID(id string) *report.ModuleHealthCheck {
	for i := range r.options.HealthChecks {
		healthCheck := &r.options.HealthChecks[i]
		if strings.HasSuffix(id, ":"+healthCheck.ID) {
			return healthCheck
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

func (r *diagnoseRunner) moduleSuccessObservations(componentID string) []*monitorpb.DoctorObservation {
	result := make([]*monitorpb.DoctorObservation, 0)
	module := strings.TrimPrefix(componentID, "moox_")
	metricName := report.ModuleMetricName(module, report.ModuleMetricLastSuccess)
	for _, item := range r.context.GetModuleObservations() {
		if item.GetComponentId() != componentID || item.GetSummary() != metricName {
			continue
		}
		labels := map[string]string{}
		if json.Unmarshal([]byte(item.GetDetailsJson()), &labels) != nil {
			continue
		}
		healthCheck := labels["health_check"]
		if healthCheck == "" || r.healthCheckEnabled(module, healthCheck) {
			result = append(result, item)
		}
	}
	return result
}

func (r *diagnoseRunner) healthCheckEnabled(module, healthCheck string) bool {
	if len(r.options.HealthChecks) == 0 {
		return true
	}
	for _, candidate := range r.options.HealthChecks {
		if candidate.Enabled && candidate.Module == module && candidate.ID == healthCheck {
			return true
		}
	}
	return false
}

func (r *diagnoseRunner) moduleExpected(module string) bool {
	if len(r.context.GetExpectedComponents()) == 0 {
		// Older/partial Monitor snapshots do not carry deployment inventory;
		// preserve functional evaluation rather than treating missing facts as
		// proof that the module is disabled.
		return true
	}
	for _, component := range r.context.GetExpectedComponents() {
		if strings.TrimPrefix(component.GetComponentId(), "moox_") == module {
			return component.GetExpected()
		}
	}
	return false
}

func (r *diagnoseRunner) moduleFreshnessThreshold(module string) time.Duration {
	threshold := 5 * time.Minute
	for _, healthCheck := range r.options.HealthChecks {
		if healthCheck.Enabled && healthCheck.Module == module && healthCheck.MaxLag > threshold {
			threshold = healthCheck.MaxLag
		}
	}
	return threshold
}

func findObservationKind(items []*monitorpb.DoctorObservation, componentID, kind string) *monitorpb.DoctorObservation {
	for _, item := range items {
		if item.GetComponentId() == componentID && item.GetKind() == kind {
			return item
		}
	}
	return nil
}

func healthCheckIDs(config []report.ModuleHealthCheck) []string {
	ids := make([]string, 0, len(config))
	for _, healthCheck := range config {
		if healthCheck.Enabled && healthCheck.CheckWatermark {
			ids = append(ids, healthCheck.ID)
		}
	}
	return ids
}

func moduleFreshnessEnabled(config []report.ModuleHealthCheck, module string) bool {
	for _, healthCheck := range config {
		if healthCheck.Enabled && healthCheck.CheckFreshness && healthCheck.Module == module {
			return true
		}
	}
	return false
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
