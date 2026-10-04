package doctor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
	"gopkg.in/yaml.v3"
)

type DeploymentClient interface {
	ListDeployments(context.Context, string) ([]*adminpb.ServiceDeployment, error)
}

type DoctorContextClient interface {
	GetDoctorContext(context.Context, *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error)
}

type BootstrapOptions struct {
	NodeID, LocalNodeID, ReleaseRoot, SeedPath, DatasetHealthPolicyPath string
	CheckIDs                                                            []string
	Client                                                              DeploymentClient
	MonitorClient                                                       DoctorContextClient
	StorageActivation                                                   StorageActivationClient
	Prober                                                              HTTPProber
	Now                                                                 func() time.Time
	ProbeWritable                                                       func(context.Context, string, string) error
	ProcessAlive                                                        func(string) bool
}

type bootstrapRunner struct {
	options                BootstrapOptions
	manifest               core.Manifest
	deployments            map[string]*adminpb.ServiceDeployment
	loadErr                error
	seedServices           map[string]seedService
	seedErr                error
	datasetHealthPolicy    report.DatasetHealthPolicy
	datasetHealthPolicyErr error
	healthChecks           []report.ModuleHealthCheck
	manifestErr            error
	delivery               *monitorpb.GetDoctorContextRsp
	deliveryErr            error
}

func RunBootstrap(ctx context.Context, options BootstrapOptions) (core.Report, error) {
	if options.NodeID == "" {
		options.NodeID = options.LocalNodeID
	}
	if options.NodeID == "" || options.LocalNodeID == "" || options.NodeID != options.LocalNodeID {
		return core.Report{}, fmt.Errorf("bootstrap only accepts the local node %q", options.LocalNodeID)
	}
	manifest, err := core.LoadEmbeddedManifest()
	if err != nil {
		return core.Report{}, err
	}
	runner := &bootstrapRunner{options: options, manifest: manifest, deployments: map[string]*adminpb.ServiceDeployment{}}
	if options.ReleaseRoot == "" {
		runner.manifestErr = fmt.Errorf("release root is required")
	} else {
		releaseManifest, manifestErr := core.LoadManifestFile(filepath.Join(options.ReleaseRoot, "config", "doctor", "components.yaml"))
		runner.manifestErr = manifestErr
		if manifestErr == nil && releaseManifest.Checksum != manifest.Checksum {
			runner.manifestErr = fmt.Errorf("release manifest checksum %s does not match embedded checksum %s", releaseManifest.Checksum, manifest.Checksum)
		}
		if runner.manifestErr == nil {
			runner.manifestErr = validateManifestChecksumFile(filepath.Join(options.ReleaseRoot, "config", "doctor", "components.yaml.sha256"), manifest.Checksum)
		}
	}
	if options.Client == nil {
		runner.loadErr = fmt.Errorf("SysDeploy client is unavailable")
	} else {
		rows, loadErr := options.Client.ListDeployments(ctx, options.NodeID)
		runner.loadErr = loadErr
		for _, row := range rows {
			if row != nil {
				runner.deployments[row.GetServiceName()] = row
			}
		}
	}
	runner.seedServices, runner.seedErr = loadSeedServices(options.SeedPath)
	runner.healthChecks = report.BuiltInModuleHealthChecks()
	runner.datasetHealthPolicy, runner.datasetHealthPolicyErr = report.LoadDatasetHealthPolicy(options.DatasetHealthPolicyPath)
	specs := bootstrapSpecs(manifest, options.NodeID)
	specs, err = selectSpecs(specs, options.CheckIDs)
	if err != nil {
		return core.Report{}, err
	}
	if options.MonitorClient != nil && hasCheck(specs, "monitor.metrics_delivery") {
		runner.delivery, runner.deliveryErr = options.MonitorClient.GetDoctorContext(ctx, &monitorpb.GetDoctorContextReq{NodeId: options.NodeID, HealthCheckIds: healthCheckIDs(runner.healthChecks)})
	}
	report, err := (core.Engine{Mode: core.ModeBootstrap, Now: options.Now}).Run(ctx, specs, core.RunnerFunc(runner.run))
	if err != nil {
		return core.Report{}, err
	}
	report.RunID = newRunID()
	report.ManifestChecksum = manifest.Checksum
	return report, nil
}

func hasCheck(specs []core.CheckSpec, id string) bool {
	for _, spec := range specs {
		if spec.ID == id {
			return true
		}
	}
	return false
}

func bootstrapSpecs(manifest core.Manifest, nodeID string) []core.CheckSpec {
	specs := []core.CheckSpec{
		{ID: "bootstrap.release_contract"},
		{ID: "bootstrap.inventory", RequiredDependencies: []string{"bootstrap.release_contract"}},
		{ID: storageDatasetActivationCheckID, Timeout: storageActivationCheckTimeout},
	}
	healthChecks := map[string]string{}
	for _, component := range manifest.Components {
		scope := component.ComponentID + "@" + nodeID
		inventory := []string{"bootstrap.inventory"}
		specs = append(specs,
			core.CheckSpec{ID: "bootstrap.service_identity:" + scope, RequiredDependencies: inventory},
			core.CheckSpec{ID: "bootstrap.network:" + scope, RequiredDependencies: inventory},
			core.CheckSpec{ID: "bootstrap.path_permissions:" + scope, RequiredDependencies: inventory},
			core.CheckSpec{ID: "bootstrap.service_autostart:" + scope, RequiredDependencies: inventory},
			core.CheckSpec{ID: "service.health:" + scope, RequiredDependencies: inventory},
		)
		healthChecks[component.ComponentID] = "service.health:" + scope
	}
	metricsDeps := []string{}
	for _, id := range []string{"eventbus", "moox_monitor"} {
		if healthChecks[id] != "" {
			metricsDeps = append(metricsDeps, healthChecks[id])
		}
	}
	specs = append(specs, core.CheckSpec{ID: "monitor.metrics_delivery", RequiredDependencies: metricsDeps})
	for _, component := range manifest.Components {
		specs = append(specs, core.CheckSpec{ID: "monitor.reporter_coverage:" + component.ComponentID, RequiredDependencies: []string{"monitor.metrics_delivery"}, OptionalDependencies: []string{healthChecks[component.ComponentID]}})
	}
	return specs
}

func (r *bootstrapRunner) run(ctx context.Context, spec core.CheckSpec, _ []core.DependencyContext) core.CheckResult {
	result := core.CheckResult{ID: spec.ID}
	switch spec.ID {
	case "bootstrap.release_contract":
		contractErr := r.manifestErr
		if contractErr == nil {
			contractErr = r.seedErr
		}
		if contractErr == nil {
			contractErr = r.datasetHealthPolicyErr
		}
		if contractErr != nil {
			return checkResult(spec.ID, core.StatusFail, "release contract is incomplete", contractErr, "apply_service_deployments_seed")
		}
		if err := validateSeedAgainstManifest(r.manifest, r.seedServices); err != nil {
			return checkResult(spec.ID, core.StatusFail, "deployment seed does not match the Manifest", err, "apply_service_deployments_seed")
		}
		return checkResult(spec.ID, core.StatusPass, "release Manifest, checksum, deployment seed, and Dataset health policy are available", nil)
	case "bootstrap.inventory":
		if r.loadErr != nil {
			return checkResult(spec.ID, core.StatusFail, "SysDeploy inventory is unavailable", r.loadErr, "apply_service_deployments_seed")
		}
		missing := []string{}
		for _, component := range r.manifest.Components {
			seed, seeded := r.seedServices[component.ServiceName]
			if component.RequiredInDefaultProfile && (r.deployments[component.ServiceName] == nil || !seeded || seed.Status != "active" || seed.DeploymentMode != "process") {
				missing = append(missing, component.ServiceName)
			}
		}
		if len(missing) > 0 {
			return checkResult(spec.ID, core.StatusFail, "required deployment inventory is missing: "+strings.Join(missing, ", "), nil, "apply_service_deployments_seed")
		}
		return checkResult(spec.ID, core.StatusPass, "deployment inventory matches the V1 Manifest", nil)
	}
	if spec.ID == "monitor.metrics_delivery" {
		if r.deliveryErr != nil || r.delivery == nil {
			err := r.deliveryErr
			if err == nil {
				err = fmt.Errorf("Monitor delivery context was not requested")
			}
			return checkResult(spec.ID, core.StatusUnknown, "Reporter delivery cannot be confirmed without a bounded Monitor context", err, "run_bootstrap")
		}
		for _, observation := range append(append([]*monitorpb.DoctorObservation{}, r.delivery.GetReporterObservations()...), r.delivery.GetMissingObservations()...) {
			if observation.GetComponentId() == "moox_monitor" && (observation.GetConflict() || observation.GetStatus() == "FAIL" || observation.GetStale()) {
				return checkResult(spec.ID, core.StatusFail, "Monitor Reporter delivery fact is stale or conflicting", nil, "verify_eventbus_credentials")
			}
		}
		for _, observation := range r.delivery.GetReporterObservations() {
			if observation.GetComponentId() == "moox_monitor" && observation.GetStatus() == "FRESH" && !observation.GetStale() {
				return checkResult(spec.ID, core.StatusPass, "Monitor Reporter delivery fact is current", nil)
			}
		}
		return checkResult(spec.ID, core.StatusUnknown, "Monitor Reporter delivery fact is missing", nil, "verify_eventbus_credentials")
	}
	if spec.ID == storageDatasetActivationCheckID {
		return r.runStorageDatasetActivation(ctx, spec)
	}
	component, kind, ok := r.componentForCheck(spec.ID)
	if !ok {
		return checkResult(spec.ID, core.StatusFail, "unknown bootstrap check", nil)
	}
	deployment := r.deployments[component.ServiceName]
	if deployment == nil || deployment.GetStatus() != "active" {
		return checkResult(spec.ID, core.StatusSkipped, "component is disabled or not expected on this node", nil)
	}
	switch kind {
	case "service_identity":
		if component.FunctionalObservability == core.FunctionalObservabilityDeferred || component.Transport != core.TransportReporter {
			return checkResult(spec.ID, core.StatusSkipped, "identity extension is not part of the active V1 contract", nil)
		}
		probe, err := r.options.Prober.Get(ctx, healthURL(deployment, component.HealthPath))
		if err != nil {
			return checkResult(spec.ID, core.StatusFail, "service identity probe failed", err, "verify_service_identity")
		}
		var identity struct {
			Service                 string `json:"service"`
			InstanceID              string `json:"instance_id"`
			NodeID                  string `json:"node_id"`
			BootID                  string `json:"boot_id"`
			DatasetHealthPolicyHash string `json:"dataset_health_policy_hash"`
		}
		if err := json.Unmarshal(probe.Body, &identity); err != nil {
			return checkResult(spec.ID, core.StatusFail, "service identity response is invalid", err, "verify_service_identity")
		}
		want := component.ServiceName + "@" + r.options.NodeID
		identityMismatch := identity.Service != component.ServiceName ||
			identity.InstanceID != want ||
			identity.NodeID != r.options.NodeID ||
			identity.BootID == ""
		policyMismatch := component.ServiceName == "moox_monitor" &&
			r.datasetHealthPolicy.Checksum != "" &&
			identity.DatasetHealthPolicyHash != r.datasetHealthPolicy.Checksum
		if identityMismatch || policyMismatch {
			return checkResult(spec.ID, core.StatusFail, "service identity conflicts with canonical service@node identity", nil, "verify_service_identity")
		}
		result = checkResult(spec.ID, core.StatusPass, "service identity matches the canonical contract", nil)
		result.Observations = []core.Observation{{Source: "health", ObservedAt: probe.ObservedAt, Summary: "signed identity response", Digest: probe.Digest}}
		return result
	case "network", "health":
		probe, err := r.options.Prober.Get(ctx, healthURL(deployment, component.HealthPath))
		if err != nil {
			return checkResult(spec.ID, core.StatusFail, "service health endpoint is unavailable", err, "restart_service_manually")
		}
		result = checkResult(spec.ID, core.StatusPass, "service health endpoint responded", nil)
		result.Observations = []core.Observation{{Source: "health", ObservedAt: probe.ObservedAt, Summary: "signed health response", Digest: probe.Digest}}
		return result
	case "path_permissions":
		if len(component.WritablePaths) == 0 {
			return checkResult(spec.ID, core.StatusSkipped, "component declares no writable paths", nil)
		}
		for _, path := range component.WritablePaths {
			probeWritable := r.options.ProbeWritable
			if probeWritable == nil {
				probeWritable = ProbeWritablePath
			}
			if err := probeWritable(ctx, r.options.ReleaseRoot, path); err != nil {
				return checkResult(spec.ID, core.StatusFail, "writable path probe failed", err, "repair_path_permissions")
			}
		}
		return checkResult(spec.ID, core.StatusPass, "declared writable paths accept bounded temporary probes", nil)
	case "service_autostart":
		pidPath := filepath.Join(r.options.ReleaseRoot, "run", processPIDName(component.ServiceName)+".pid")
		isAlive := r.options.ProcessAlive
		if isAlive == nil {
			isAlive = processAlive
		}
		if isAlive(pidPath) {
			return checkResult(spec.ID, core.StatusPass, "service process is running", nil)
		}
		return checkResult(spec.ID, core.StatusWarn, "service PID is not active; verify the configured service manager", nil, "restart_service_manually")
	case "reporter_coverage":
		if component.FunctionalObservability == core.FunctionalObservabilityDeferred || component.FunctionalObservability == core.FunctionalObservabilityNotApplicable {
			if component.FunctionalObservability == core.FunctionalObservabilityNotApplicable {
				return checkResult(spec.ID, core.StatusSkipped, "functional_observability_not_applicable", nil)
			}
			return checkResult(spec.ID, core.StatusSkipped, "storage_observability_deferred", nil)
		}
		if component.Transport != core.TransportReporter {
			return checkResult(spec.ID, core.StatusSkipped, "component does not use Reporter transport", nil)
		}
		probe, err := r.options.Prober.Get(ctx, healthURL(deployment, "/metrics"))
		if err != nil {
			return checkResult(spec.ID, core.StatusFail, "Reporter metrics endpoint is unavailable", err, "verify_eventbus_credentials")
		}
		if len(probe.Body) == 0 {
			return checkResult(spec.ID, core.StatusFail, "Reporter metrics endpoint is empty", nil, "verify_eventbus_credentials")
		}
		if reporterHasRecentFailure(probe.Body, probe.ObservedAt) {
			return checkResult(spec.ID, core.StatusFail, "Reporter has recorded delivery failures", nil, "verify_eventbus_credentials")
		}
		return checkResult(spec.ID, core.StatusPass, "Reporter endpoint is readable", nil)
	}
	return result
}

func reporterHasRecentFailure(body []byte, now time.Time) bool {
	var total, lastError float64
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		name := strings.SplitN(fields[0], "{", 2)[0]
		switch {
		case strings.HasPrefix(name, "moox_") &&
			(strings.HasSuffix(name, "_report_errors_total") || strings.HasSuffix(name, "_metrics_errors_total")):
			if value > total {
				total = value
			}
		case strings.HasPrefix(name, "moox_") &&
			(strings.HasSuffix(name, "_report_last_error_timestamp_seconds") || strings.HasSuffix(name, "_metrics_last_error_timestamp_seconds")):
			if value > lastError {
				lastError = value
			}
		}
	}
	if total <= 0 {
		return false
	}
	if lastError == 0 {
		return true
	}
	return now.Sub(time.Unix(int64(lastError), 0).UTC()) <= 2*time.Minute
}

func (r *bootstrapRunner) componentForCheck(id string) (core.Component, string, bool) {
	for _, component := range r.manifest.Components {
		if id == "monitor.reporter_coverage:"+component.ComponentID {
			return component, "reporter_coverage", true
		}
		suffix := ":" + component.ComponentID + "@" + r.options.NodeID
		for _, kind := range []string{"service_identity", "network", "path_permissions", "service_autostart"} {
			if id == "bootstrap."+kind+suffix {
				return component, kind, true
			}
		}
		if id == "service.health"+suffix {
			return component, "health", true
		}
	}
	return core.Component{}, "", false
}

type seedService struct {
	Name           string `yaml:"name"`
	DeploymentMode string `yaml:"deployment_mode"`
	Status         string `yaml:"status"`
}

func loadSeedServices(path string) (map[string]seedService, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 2<<20 {
		return nil, fmt.Errorf("deployment seed exceeds 2 MiB")
	}
	var seed struct {
		Version  int           `yaml:"version"`
		Services []seedService `yaml:"services"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&seed); err != nil {
		return nil, err
	}
	if seed.Version != 1 {
		return nil, fmt.Errorf("unsupported deployment seed version %d", seed.Version)
	}
	services := map[string]seedService{}
	for _, service := range seed.Services {
		if service.Name == "" {
			return nil, fmt.Errorf("deployment seed contains an empty service name")
		}
		if _, exists := services[service.Name]; exists {
			return nil, fmt.Errorf("deployment seed contains duplicate service %q", service.Name)
		}
		services[service.Name] = service
	}
	return services, nil
}

func validateSeedAgainstManifest(manifest core.Manifest, services map[string]seedService) error {
	known := make(map[string]bool, len(manifest.Components))
	for _, component := range manifest.Components {
		known[component.ServiceName] = true
		seed, ok := services[component.ServiceName]
		if component.RequiredInDefaultProfile && (!ok || seed.DeploymentMode != "process" || seed.Status != "active") {
			return fmt.Errorf("required service %q must be an active process in the deployment seed", component.ServiceName)
		}
	}
	for name, service := range services {
		if service.Status == "active" && service.DeploymentMode == "process" && !known[name] {
			return fmt.Errorf("active process %q is missing from the Manifest", name)
		}
	}
	return nil
}

func validateManifestChecksumFile(path, want string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read doctor manifest checksum: %w", err)
	}
	value := strings.TrimSpace(string(raw))
	if value != want && value != strings.TrimPrefix(want, "sha256:") {
		return fmt.Errorf("doctor manifest checksum file contains %q, want %s", value, want)
	}
	return nil
}

func healthURL(deployment *adminpb.ServiceDeployment, path string) string {
	var extra struct {
		HealthURL string `json:"health_url"`
	}
	_ = json.Unmarshal([]byte(deployment.GetExtraConfig()), &extra)
	if extra.HealthURL != "" {
		if path == "/metrics" {
			return strings.TrimSuffix(strings.TrimSuffix(extra.HealthURL, "/readyz"), "/healthz") + path
		}
		return extra.HealthURL
	}
	return "http://" + deployment.GetHost() + ":" + strconv.Itoa(int(deployment.GetPort())) + path
}

func checkResult(id string, status core.CheckStatus, summary string, err error, actions ...string) core.CheckResult {
	result := core.CheckResult{ID: id, Status: status, Summary: summary, RecoveryActionIDs: actions}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func processPIDName(service string) string {
	return map[string]string{"admin_gateway": "admin", "web_host": "web-host", "storage-primary": "storage-primary", "storage-view": "storage-view", "eventbus": "eventbus", "moox_gateway": "gateway", "moox_monitor": "monitor", "moox_collector": "collector", "moox_cloudnode": "cloudnode", "moox_factor": "factor", "moox_strategy": "strategy", "moox_trade": "trade", "moox_archive": "archive", "moox_hostagent": "host-agent"}[service]
}

func processAlive(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func newRunID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("doctor-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func selectSpecs(specs []core.CheckSpec, selected []string) ([]core.CheckSpec, error) {
	if len(selected) == 0 {
		return specs, nil
	}
	byID := map[string]core.CheckSpec{}
	for _, spec := range specs {
		byID[spec.ID] = spec
	}
	wanted := map[string]bool{}
	var add func(string) error
	add = func(id string) error {
		spec, ok := byID[id]
		if !ok {
			return fmt.Errorf("unknown check id %q", id)
		}
		if wanted[id] {
			return nil
		}
		wanted[id] = true
		for _, dependency := range append(append([]string{}, spec.RequiredDependencies...), spec.OptionalDependencies...) {
			if err := add(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range selected {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	out := make([]core.CheckSpec, 0, len(wanted))
	for _, spec := range specs {
		if wanted[spec.ID] {
			out = append(out, spec)
		}
	}
	return out, nil
}
