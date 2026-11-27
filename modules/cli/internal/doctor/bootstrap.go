package doctor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

type DeploymentClient interface {
	ListDeployments(context.Context, string) ([]Deployment, error)
}

type DoctorContextClient interface {
	GetDoctorContext(context.Context, *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error)
}

type BootstrapOptions struct {
	NodeID, LocalNodeID, DatasetHealthPolicyPath string
	CheckIDs                                     []string
	Client                                       DeploymentClient
	MonitorClient                                DoctorContextClient
	// Probe 请求组件只听本机的健康端口：查的是本机时直接请求 127.0.0.1，带远端 --node 时经 SSH 转发到目标机的 127.0.0.1。
	// SSH 连不上时 bootstrap 把这一项记为失败并建议重启服务（diagnose 同样情况记为无法判断）。
	Probe         HealthProbe
	Now           func() time.Time
	ProbeWritable func(context.Context, string, string) error
	// Systemd 读组件 systemd 用户服务（moox-<组件>.service）的状态：开机自启、运行中与 linger 三项都满足才通过；
	// 为空时自启动检查记为无法判断。全部组件都由 systemd 用户服务托管。
	Systemd SystemdChecker
	// DeployRoot 是部署根（默认 /data/moox）：部署记录是 <部署根>/version.json，组件的数据目录是 <部署根>/data/<组件>。
	DeployRoot string
	// ReadFile 读节点上的一个文件（本机直接读，远端经 SSH）；为空时读本机文件。
	ReadFile func(ctx context.Context, nodeID, path string) ([]byte, error)
}

type bootstrapRunner struct {
	options                BootstrapOptions
	manifest               core.Manifest
	deployments            map[string]Deployment
	loadErr                error
	datasetHealthPolicy    report.DatasetHealthPolicy
	datasetHealthPolicyErr error
	healthChecks           []report.ModuleHealthCheck
	manifestErr            error
	delivery               *monitorpb.GetDoctorContextRsp
	deliveryErr            error
}

func isRemoteNode(options BootstrapOptions) bool {
	return options.LocalNodeID == "" || options.NodeID != options.LocalNodeID
}

func joinNodePath(remote bool, elem ...string) string {
	if remote {
		return path.Join(elem...)
	}
	return filepath.Join(elem...)
}

func RunBootstrap(ctx context.Context, options BootstrapOptions) (core.Report, error) {
	if options.NodeID == "" {
		options.NodeID = options.LocalNodeID
	}
	if options.NodeID == "" {
		return core.Report{}, fmt.Errorf("bootstrap requires a target node ID")
	}
	if isRemoteNode(options) && (options.ReadFile == nil || options.Probe == nil || options.ProbeWritable == nil) {
		return core.Report{}, fmt.Errorf("bootstrap for remote node %q requires remote SSH capabilities (load moox.toml)", options.NodeID)
	}
	manifest, err := core.LoadEmbeddedManifest()
	if err != nil {
		return core.Report{}, err
	}
	runner := &bootstrapRunner{options: options, manifest: manifest, deployments: map[string]Deployment{}}
	if options.DeployRoot == "" {
		runner.manifestErr = fmt.Errorf("没有部署根，无法核对部署记录")
	} else {
		runner.manifestErr = verifyDeployedCatalog(ctx, options, manifest.Checksum)
	}
	if options.Client == nil {
		runner.loadErr = fmt.Errorf("SysDeploy client is unavailable")
	} else {
		rows, loadErr := options.Client.ListDeployments(ctx, options.NodeID)
		runner.loadErr = loadErr
		for _, row := range rows {
			runner.deployments[row.ComponentID] = row
		}
	}
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
	for _, id := range []string{"eventbus", "monitor"} {
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
			contractErr = r.datasetHealthPolicyErr
		}
		if contractErr != nil {
			return checkResult(spec.ID, core.StatusFail, "release contract is incomplete", contractErr, "sync_host_deployments")
		}
		return checkResult(spec.ID, core.StatusPass, "deployed catalog checksum matches and Dataset health policy is available", nil)
	case "bootstrap.inventory":
		if r.loadErr != nil {
			return checkResult(spec.ID, core.StatusFail, "SysDeploy inventory is unavailable", r.loadErr, "sync_host_deployments")
		}
		missing := []string{}
		required := []string{"host-gateway", "host-agent"}
		if _, control := r.deployments["admin"]; control {
			required = append(required, "admin")
		}
		for _, id := range required {
			if _, exists := r.deployments[id]; !exists {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			return checkResult(spec.ID, core.StatusFail, "required deployment inventory is missing: "+strings.Join(missing, ", "), nil, "sync_host_deployments")
		}
		return checkResult(spec.ID, core.StatusPass, "host deployments match the component catalog", nil)
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
			if observation.GetComponentId() == "monitor" && (observation.GetConflict() || observation.GetStatus() == "FAIL" || observation.GetStale()) {
				return checkResult(spec.ID, core.StatusFail, "Monitor Reporter delivery fact is stale or conflicting", nil, "verify_eventbus_credentials")
			}
		}
		for _, observation := range r.delivery.GetReporterObservations() {
			if observation.GetComponentId() == "monitor" && observation.GetStatus() == "FRESH" && !observation.GetStale() {
				return checkResult(spec.ID, core.StatusPass, "Monitor Reporter delivery fact is current", nil)
			}
		}
		return checkResult(spec.ID, core.StatusUnknown, "Monitor Reporter delivery fact is missing", nil, "verify_eventbus_credentials")
	}
	component, kind, ok := r.componentForCheck(spec.ID)
	if !ok {
		return checkResult(spec.ID, core.StatusFail, "unknown bootstrap check", nil)
	}
	deployment, placed := r.deployments[component.ComponentID]
	if !placed || deployment.Status != "enabled" {
		return checkResult(spec.ID, core.StatusSkipped, "component is disabled or not expected on this node", nil)
	}
	switch kind {
	case "service_identity":
		if component.FunctionalObservability == core.FunctionalObservabilityDeferred || component.Transport != core.TransportReporter {
			return checkResult(spec.ID, core.StatusSkipped, "identity extension is not part of the active V1 contract", nil)
		}
		probe, err := r.probe(ctx, component, "/readyz")
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
		policyMismatch := component.ServiceName == "monitor" &&
			r.datasetHealthPolicy.Checksum != "" &&
			identity.DatasetHealthPolicyHash != r.datasetHealthPolicy.Checksum
		if identityMismatch || policyMismatch {
			return checkResult(spec.ID, core.StatusFail, "service identity conflicts with canonical service@node identity", nil, "verify_service_identity")
		}
		result = checkResult(spec.ID, core.StatusPass, "service identity matches the canonical contract", nil)
		result.Observations = []core.Observation{{Source: "health", ObservedAt: probe.ObservedAt, Summary: "local identity response", Digest: probe.Digest}}
		return result
	case "network", "health":
		probe, err := r.probe(ctx, component, "/readyz")
		if err != nil {
			return checkResult(spec.ID, core.StatusFail, "service health endpoint is unavailable", err, "restart_service_manually")
		}
		result = checkResult(spec.ID, core.StatusPass, "service health endpoint responded", nil)
		result.Observations = []core.Observation{{Source: "health", ObservedAt: probe.ObservedAt, Summary: "local health response", Digest: probe.Digest}}
		return result
	case "path_permissions":
		// 每个组件的可写目录固定是 <部署根>/data/<组件>（systemd 单元的 WorkingDirectory），不用声明。
		if r.options.DeployRoot == "" {
			return checkResult(spec.ID, core.StatusWarn, "无法确定部署根目录，没有检查数据目录", nil, "repair_path_permissions")
		}
		probeWritable := r.options.ProbeWritable
		if probeWritable == nil {
			probeWritable = ProbeWritablePath
		}
		if err := probeWritable(ctx, r.options.DeployRoot, joinNodePath(isRemoteNode(r.options), "data", component.ComponentID)); err != nil {
			return checkResult(spec.ID, core.StatusFail, "data directory probe failed", err, "repair_path_permissions")
		}
		return checkResult(spec.ID, core.StatusPass, "data directory accepts bounded temporary probes", nil)
	case "service_autostart":
		unit := servicecatalog.UnitName(component.ComponentID)
		if r.options.Systemd == nil {
			return checkResult(spec.ID, core.StatusUnknown, "systemd state checker is unavailable for this run", nil, "restart_service_manually")
		}
		state, err := r.options.Systemd(ctx, r.options.NodeID, unit)
		return supervisorResult(spec.ID, unit, state, err)
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
		probe, err := r.probe(ctx, component, "/metrics")
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

// probe 请求 --node 所指节点上组件的健康端口（远端节点经 SSH 转发）。
func (r *bootstrapRunner) probe(ctx context.Context, component core.Component, path string) (ProbeResult, error) {
	if r.options.Probe == nil {
		return ProbeResult{}, fmt.Errorf("health probe is unavailable")
	}
	return r.options.Probe(ctx, r.options.NodeID, component.HealthPort, path)
}

func checkResult(id string, status core.CheckStatus, summary string, err error, actions ...string) core.CheckResult {
	result := core.CheckResult{ID: id, Status: status, Summary: summary, RecoveryActionIDs: actions}
	if err != nil {
		result.Error = err.Error()
	}
	return result
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

// verifyDeployedCatalog 核对节点上 <部署根>/version.json 记的服务目录摘要与这个 moox-cli 内嵌的一致：
// 不一致说明节点上的组件和这个 CLI 不是同一版本，按 CLI 的目录诊断会得出错误的结论。
func verifyDeployedCatalog(ctx context.Context, options BootstrapOptions, checksum string) error {
	versionPath := joinNodePath(isRemoteNode(options), options.DeployRoot, "version.json")
	read := options.ReadFile
	if read == nil {
		read = func(_ context.Context, _ string, target string) ([]byte, error) { return os.ReadFile(target) }
	}
	raw, err := read(ctx, options.NodeID, versionPath)
	if err != nil {
		return fmt.Errorf("读不到部署记录 %s（这台主机还没用 setup deploy 装过？）: %w", versionPath, err)
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("部署记录 %s 超过 64 KiB", versionPath)
	}
	var version struct {
		CatalogSHA256 string `json:"catalog_sha256"`
	}
	if err := json.Unmarshal(raw, &version); err != nil {
		return fmt.Errorf("部署记录 %s 不是合法的 JSON: %w", versionPath, err)
	}
	if version.CatalogSHA256 != checksum {
		return fmt.Errorf("节点上部署的服务目录摘要 %q 与这个 moox-cli 内嵌的 %s 不一致：重新部署这台主机，或换成同一版本的 CLI", version.CatalogSHA256, checksum)
	}
	return nil
}
