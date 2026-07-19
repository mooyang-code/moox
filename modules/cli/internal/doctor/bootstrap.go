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
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
	"gopkg.in/yaml.v3"
)

type DeploymentClient interface {
	ListDeployments(context.Context, string) ([]*adminpb.ServiceDeployment, error)
}

type BootstrapOptions struct {
	NodeID, LocalNodeID, ReleaseRoot, SeedPath, PipelinePath string
	CheckIDs                                                 []string
	Client                                                   DeploymentClient
	Prober                                                   HTTPProber
	Now                                                      func() time.Time
}

type bootstrapRunner struct {
	options     BootstrapOptions
	manifest    core.Manifest
	deployments map[string]*adminpb.ServiceDeployment
	loadErr     error
	seedNames   map[string]bool
	seedErr     error
	pipelineErr error
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
	runner.seedNames, runner.seedErr = loadSeedNames(options.SeedPath)
	_, runner.pipelineErr = report.LoadPipelineAllowlist(options.PipelinePath)
	specs := bootstrapSpecs(manifest, options.NodeID)
	specs, err = selectSpecs(specs, options.CheckIDs)
	if err != nil {
		return core.Report{}, err
	}
	report, err := (core.Engine{Mode: core.ModeBootstrap, Now: options.Now}).Run(ctx, specs, core.RunnerFunc(runner.run))
	if err != nil {
		return core.Report{}, err
	}
	report.RunID = newRunID()
	report.ManifestChecksum = manifest.Checksum
	return report, nil
}

func bootstrapSpecs(manifest core.Manifest, nodeID string) []core.CheckSpec {
	specs := []core.CheckSpec{{ID: "bootstrap.release_contract"}, {ID: "bootstrap.inventory", RequiredDependencies: []string{"bootstrap.release_contract"}}}
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
		if r.seedErr != nil || r.pipelineErr != nil {
			contractErr := r.seedErr
			if contractErr == nil {
				contractErr = r.pipelineErr
			}
			return checkResult(spec.ID, core.StatusFail, "release contract is incomplete", contractErr, "apply_service_deployments_seed")
		}
		return checkResult(spec.ID, core.StatusPass, "embedded Manifest, deployment seed, and pipeline allowlist are available", nil)
	case "bootstrap.inventory":
		if r.loadErr != nil {
			return checkResult(spec.ID, core.StatusFail, "SysDeploy inventory is unavailable", r.loadErr, "apply_service_deployments_seed")
		}
		missing := []string{}
		for _, component := range r.manifest.Components {
			if component.RequiredInDefaultProfile && (r.deployments[component.ServiceName] == nil || !r.seedNames[component.ServiceName]) {
				missing = append(missing, component.ServiceName)
			}
		}
		if len(missing) > 0 {
			return checkResult(spec.ID, core.StatusFail, "required deployment inventory is missing: "+strings.Join(missing, ", "), nil, "apply_service_deployments_seed")
		}
		return checkResult(spec.ID, core.StatusPass, "deployment inventory matches the V1 Manifest", nil)
	}
	if spec.ID == "monitor.metrics_delivery" {
		return checkResult(spec.ID, core.StatusPass, "EventBus and Monitor health dependencies passed", nil)
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
			Service    string `json:"service"`
			InstanceID string `json:"instance_id"`
			NodeID     string `json:"node_id"`
			BootID     string `json:"boot_id"`
		}
		if err := json.Unmarshal(probe.Body, &identity); err != nil {
			return checkResult(spec.ID, core.StatusFail, "service identity response is invalid", err, "verify_service_identity")
		}
		want := component.ServiceName + "@" + r.options.NodeID
		if identity.Service != component.ServiceName || identity.InstanceID != want || identity.NodeID != r.options.NodeID || identity.BootID == "" {
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
			if err := ProbeWritablePath(ctx, r.options.ReleaseRoot, path); err != nil {
				return checkResult(spec.ID, core.StatusFail, "writable path probe failed", err, "repair_path_permissions")
			}
		}
		return checkResult(spec.ID, core.StatusPass, "declared writable paths accept bounded temporary probes", nil)
	case "service_autostart":
		pidPath := filepath.Join(r.options.ReleaseRoot, "run", processPIDName(component.ServiceName)+".pid")
		if processAlive(pidPath) {
			return checkResult(spec.ID, core.StatusPass, "service process is running", nil)
		}
		return checkResult(spec.ID, core.StatusWarn, "service PID is not active; verify the configured service manager", nil, "restart_service_manually")
	case "reporter_coverage":
		if component.FunctionalObservability == core.FunctionalObservabilityDeferred {
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
		return checkResult(spec.ID, core.StatusPass, "Reporter endpoint is readable", nil)
	}
	return result
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

func loadSeedNames(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 2<<20 {
		return nil, fmt.Errorf("deployment seed exceeds 2 MiB")
	}
	var seed struct {
		Services []struct {
			Name string `yaml:"name"`
		} `yaml:"services"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&seed); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, service := range seed.Services {
		names[service.Name] = true
	}
	return names, nil
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
