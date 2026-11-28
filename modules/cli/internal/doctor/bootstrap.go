package doctor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

type DeploymentClient interface {
	ListDeployments(context.Context, string) ([]Deployment, error)
}

// HealthOverviewClient 读取 Monitor 的健康概览，自检第二部分直接复用 Monitor 的检查结果，不在 CLI 侧重算健康。
type HealthOverviewClient interface {
	GetHealthOverview(ctx context.Context, spaceID string) (*monitorpb.HealthOverview, error)
}

type BootstrapOptions struct {
	NodeID, LocalNodeID string
	CheckIDs            []string
	Client              DeploymentClient
	MonitorClient       HealthOverviewClient
	// Probe 请求组件只听本机的健康端口：查的是本机时直接请求 127.0.0.1，带远端 --node 时经 SSH 转发到目标机的 127.0.0.1。
	// SSH 连不上目标机时记为无法判断（StatusUnknown）；端口连不上或返回非 2xx 时记为失败。
	Probe         HealthProbe
	Now           func() time.Time
	ProbeWritable func(context.Context, string, string) error
	// Systemd 读组件 systemd 用户服务（moox-<组件>.service）的状态：开机自启、运行中与 linger 三项都满足才通过；
	// 为空时自启动与托管状态检查记为无法判断。全部组件都由 systemd 用户服务托管。
	Systemd SystemdChecker
	// DeployRoot 是部署根（默认 /data/moox）：部署记录是 <部署根>/version.json，组件的数据目录是 <部署根>/data/<组件>。
	DeployRoot string
	// ReadFile 读节点上的一个文件（本机直接读，远端经 SSH）；为空时读本机文件。
	ReadFile func(ctx context.Context, nodeID, path string) ([]byte, error)
}

type bootstrapRunner struct {
	options     BootstrapOptions
	manifest    core.Manifest
	deployments map[string]Deployment
	loadErr     error
	manifestErr error
	overview    *monitorpb.HealthOverview
	overviewErr error
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

// RunBootstrap 对单台主机执行安装后自检，包含两部分内容：
//  1. 本机/远端事实探测：版本摘要、主机清单、服务身份、网络、路径权限、自启与 systemd 状态、本机就绪端口；
//  2. Monitor 检查结果（GetHealthOverview）：各组件是否已在上报、是否就绪、有没有严重告警。
func RunBootstrap(ctx context.Context, options BootstrapOptions) (core.Report, error) {
	if options.NodeID == "" {
		options.NodeID = options.LocalNodeID
	}
	if options.NodeID == "" {
		return core.Report{}, fmt.Errorf("自检必须指定目标主机 ID（--node）")
	}
	if isRemoteNode(options) && (options.ReadFile == nil || options.Probe == nil || options.ProbeWritable == nil) {
		return core.Report{}, fmt.Errorf("自检远端主机 %q 需要远端 SSH 能力（请先提供可读的 moox.toml）", options.NodeID)
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
		runner.loadErr = fmt.Errorf("SysDeploy 客户端不可用")
	} else {
		rows, loadErr := options.Client.ListDeployments(ctx, options.NodeID)
		runner.loadErr = loadErr
		for _, row := range rows {
			runner.deployments[row.ComponentID] = row
		}
	}
	specs := bootstrapSpecs(manifest, options.NodeID)
	specs, err = selectSpecs(specs, options.CheckIDs)
	if err != nil {
		return core.Report{}, err
	}
	if needsMonitorOverview(specs) {
		if options.MonitorClient == nil {
			runner.overviewErr = fmt.Errorf("Monitor 客户端不可用")
		} else {
			// 自检只看系统与组件级健康（mooxsys）：避免新装时未启动业务空间的标的健康或尚未闭合首根 K 线的行情探针告警误伤安装自检；
			// 业务空间的数据链路验证由后续的 setup verify 负责。
			runner.overview, runner.overviewErr = options.MonitorClient.GetHealthOverview(ctx, events.SystemSpaceID)
			if runner.overviewErr == nil && runner.overview == nil {
				runner.overviewErr = fmt.Errorf("Monitor 返回了空的健康概览")
			}
		}
	}
	report, err := (core.Engine{Mode: core.ModeBootstrap, Now: options.Now}).Run(ctx, specs, core.RunnerFunc(runner.run))
	if err != nil {
		return core.Report{}, err
	}
	report.RunID = newRunID()
	report.ManifestChecksum = manifest.Checksum
	return report, nil
}

func needsMonitorOverview(specs []core.CheckSpec) bool {
	for _, spec := range specs {
		if spec.ID == "monitor.overview" || strings.HasPrefix(spec.ID, "monitor.component:") || strings.HasPrefix(spec.ID, "monitor.alerts:") {
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
	overviewSpec := core.CheckSpec{ID: "monitor.overview"}
	if healthChecks["monitor"] != "" {
		overviewSpec.OptionalDependencies = []string{healthChecks["monitor"]}
	}
	specs = append(specs, overviewSpec)
	for _, component := range manifest.Components {
		scope := component.ComponentID + "@" + nodeID
		specs = append(specs, core.CheckSpec{
			ID:                   "monitor.component:" + scope,
			RequiredDependencies: []string{"bootstrap.inventory", "monitor.overview"},
			OptionalDependencies: []string{healthChecks[component.ComponentID]},
		})
	}
	specs = append(specs, core.CheckSpec{
		ID:                   "monitor.alerts:" + nodeID,
		RequiredDependencies: []string{"monitor.overview"},
	})
	return specs
}

func (r *bootstrapRunner) run(ctx context.Context, spec core.CheckSpec, _ []core.DependencyContext) core.CheckResult {
	switch spec.ID {
	case "bootstrap.release_contract":
		if r.manifestErr != nil {
			return checkResult(spec.ID, core.StatusFail, "部署记录版本摘要校验未通过", r.manifestErr, "sync_host_deployments")
		}
		return checkResult(spec.ID, core.StatusPass, "部署记录的服务目录摘要与当前 CLI 一致", nil)
	case "bootstrap.inventory":
		if r.loadErr != nil {
			return checkResult(spec.ID, core.StatusFail, "无法读取 SysDeploy 主机部署清单", r.loadErr, "sync_host_deployments")
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
			return checkResult(spec.ID, core.StatusFail, "主机部署清单缺少必需组件："+strings.Join(missing, ", "), nil, "sync_host_deployments")
		}
		return checkResult(spec.ID, core.StatusPass, "主机部署清单与组件目录一致", nil)
	case "monitor.overview":
		if r.overviewErr != nil || r.overview == nil {
			err := r.overviewErr
			if err == nil {
				err = fmt.Errorf("未请求 Monitor 健康概览")
			}
			return checkResult(spec.ID, core.StatusUnknown, "无法读取 Monitor 健康概览", err, "run_bootstrap")
		}
		return checkResult(spec.ID, core.StatusPass, "已读取 Monitor 健康概览", nil)
	}
	if spec.ID == "monitor.alerts:"+r.options.NodeID {
		return r.checkMonitorAlerts(spec.ID)
	}
	component, kind, ok := r.componentForCheck(spec.ID)
	if !ok {
		return checkResult(spec.ID, core.StatusFail, "未知的自检项", nil)
	}
	deployment, placed := r.deployments[component.ComponentID]
	if !placed || deployment.Status != "enabled" {
		return checkResult(spec.ID, core.StatusSkipped, "该组件未部署在本机或已停用", nil)
	}
	switch kind {
	case "service_identity":
		if component.Transport != core.TransportReporter {
			return checkResult(spec.ID, core.StatusSkipped, "该组件不走 Reporter 身份契约", nil)
		}
		probe, err := r.probe(ctx, component, "/readyz")
		if err != nil {
			if errors.Is(err, ErrNodeUnreachable) {
				return checkResult(spec.ID, core.StatusUnknown, "无法经 SSH 连到节点以核对服务身份", err, "verify_service_identity")
			}
			return checkResult(spec.ID, core.StatusFail, "服务身份探测失败", err, "verify_service_identity")
		}
		var identity struct {
			Service    string `json:"service"`
			InstanceID string `json:"instance_id"`
			NodeID     string `json:"node_id"`
			BootID     string `json:"boot_id"`
		}
		if err := json.Unmarshal(probe.Body, &identity); err != nil {
			return checkResult(spec.ID, core.StatusFail, "服务身份响应不是合法的 JSON", err, "verify_service_identity")
		}
		want := component.ServiceName + "@" + r.options.NodeID
		if identity.Service != component.ServiceName ||
			identity.InstanceID != want ||
			identity.NodeID != r.options.NodeID ||
			identity.BootID == "" {
			return checkResult(spec.ID, core.StatusFail, "服务身份与规范的 service@node 不一致", nil, "verify_service_identity")
		}
		result := checkResult(spec.ID, core.StatusPass, "服务身份与规范契约一致", nil)
		result.Observations = []core.Observation{{Source: "health", ObservedAt: probe.ObservedAt, Summary: "本机服务身份响应", Digest: probe.Digest}}
		return result
	case "network":
		probe, err := r.probe(ctx, component, "/healthz")
		if err != nil {
			if errors.Is(err, ErrNodeUnreachable) {
				return checkResult(spec.ID, core.StatusUnknown, "无法经 SSH 连到节点以探测健康端口", err, "restart_service_manually")
			}
			return checkResult(spec.ID, core.StatusFail, "本机健康端口 /healthz 不可达", err, "restart_service_manually")
		}
		result := checkResult(spec.ID, core.StatusPass, "本机健康端口 /healthz 可达", nil)
		result.Observations = []core.Observation{{Source: "health", ObservedAt: probe.ObservedAt, Summary: "本机 /healthz 响应", Digest: probe.Digest}}
		return result
	case "health":
		probe, err := r.probe(ctx, component, "/readyz")
		if err != nil {
			if errors.Is(err, ErrNodeUnreachable) {
				return checkResult(spec.ID, core.StatusUnknown, "无法经 SSH 连到节点以探测就绪端口", err, "restart_service_manually")
			}
			return checkResult(spec.ID, core.StatusFail, "本机就绪端口 /readyz 未就绪或不可达", err, "restart_service_manually")
		}
		result := checkResult(spec.ID, core.StatusPass, "本机就绪端口 /readyz 已就绪", nil)
		result.Observations = []core.Observation{{Source: "health", ObservedAt: probe.ObservedAt, Summary: "本机 /readyz 响应", Digest: probe.Digest}}
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
			return checkResult(spec.ID, core.StatusFail, "数据目录可写探测失败", err, "repair_path_permissions")
		}
		return checkResult(spec.ID, core.StatusPass, "数据目录可写探测通过", nil)
	case "service_autostart":
		unit := servicecatalog.UnitName(component.ComponentID)
		if r.options.Systemd == nil {
			return checkResult(spec.ID, core.StatusUnknown, "未提供 systemd 状态检查器，无法核对自启与托管状态", nil, "restart_service_manually")
		}
		state, err := r.options.Systemd(ctx, r.options.NodeID, unit)
		return supervisorResult(spec.ID, unit, state, err)
	case "monitor_component":
		return r.checkMonitorComponent(spec.ID, component.ComponentID)
	}
	return checkResult(spec.ID, core.StatusFail, "未知的自检项", nil)
}

// checkMonitorComponent 直接读取 Monitor 健康概览里的组件检查结论：是否已在上报、是否就绪。
func (r *bootstrapRunner) checkMonitorComponent(checkID, componentID string) core.CheckResult {
	var entry *monitorpb.HealthComponent
	for _, candidate := range r.overview.GetComponents() {
		if candidate.GetHostId() == r.options.NodeID && candidate.GetComponentId() == componentID {
			entry = candidate
			break
		}
	}
	if entry == nil {
		if !r.overview.GetTopologyKnown() {
			return checkResult(checkID, core.StatusUnknown, "Monitor 尚未同步部署拓扑", nil, "sync_host_deployments")
		}
		return checkResult(checkID, core.StatusUnknown, "Monitor 健康概览中暂无该组件记录", nil, "sync_host_deployments")
	}
	status := entry.GetStatus()
	if status == "disabled" {
		return checkResult(checkID, core.StatusSkipped, "Monitor 记为已停用", nil)
	}
	if entry.GetReadiness() == "not_ready" {
		reason := strings.TrimSpace(entry.GetNotReadyReason())
		if reason == "" {
			reason = strings.TrimSpace(entry.GetReason())
		}
		if reason == "" {
			reason = "组件报告未就绪"
		}
		return checkResult(checkID, core.StatusFail, "Monitor 报告未就绪："+reason, nil, "restart_service_manually")
	}
	if status == "down" {
		reason := strings.TrimSpace(entry.GetReason())
		if reason == "" {
			reason = "组件不可用或停止上报"
		}
		return checkResult(checkID, core.StatusFail, "Monitor 报告组件异常："+reason, nil, "verify_eventbus_credentials", "restart_service_manually")
	}
	if status == "degraded" {
		reason := strings.TrimSpace(entry.GetReason())
		if reason == "" {
			reason = "组件降级"
		}
		return checkResult(checkID, core.StatusWarn, "Monitor 报告组件降级："+reason, nil, "verify_eventbus_credentials")
	}
	if status == "unknown" {
		reason := strings.TrimSpace(entry.GetReason())
		if reason == "" {
			reason = "尚未收到监控上报"
		}
		return checkResult(checkID, core.StatusUnknown, "Monitor 尚未确认组件状态："+reason, nil, "verify_eventbus_credentials")
	}
	if reporter := entry.GetReporter(); reporter != nil {
		switch reporter.GetStatus() {
		case "", "healthy", "unchecked":
		case "unknown":
			return checkResult(checkID, core.StatusFail, "Monitor 尚未收到该组件上报："+strings.TrimSpace(reporter.GetReason()), nil, "verify_eventbus_credentials")
		default:
			return checkResult(checkID, core.StatusWarn, "Monitor 报告上报异常："+strings.TrimSpace(reporter.GetReason()), nil, "verify_eventbus_credentials")
		}
	}
	if status == "unchecked" {
		return checkResult(checkID, core.StatusPass, "Monitor 记为未配置指标上报", nil)
	}
	return checkResult(checkID, core.StatusPass, "Monitor 确认组件已上报且就绪", nil)
}

// checkMonitorAlerts 直接读取 Monitor 概览中的严重告警（critical），过滤出属于本节点或全局系统的严重告警。
func (r *bootstrapRunner) checkMonitorAlerts(checkID string) core.CheckResult {
	var critical []string
	for _, alert := range r.overview.GetAlerts() {
		if alert.GetSeverity() != AlertSeverityCritical {
			continue
		}
		if !r.alertAppliesToNode(alert.GetObject()) {
			continue
		}
		line := strings.TrimSpace(alert.GetTitle())
		if reason := strings.TrimSpace(alert.GetReason()); reason != "" {
			line += "（" + reason + "）"
		}
		if line == "" {
			line = alert.GetId()
		}
		critical = append(critical, line)
	}
	if len(critical) == 0 {
		return checkResult(checkID, core.StatusPass, "Monitor 没有严重告警", nil)
	}
	shown := critical
	suffix := ""
	if len(shown) > 3 {
		shown = shown[:3]
		suffix = fmt.Sprintf(" 等共 %d 条", len(critical))
	}
	return checkResult(checkID, core.StatusFail,
		fmt.Sprintf("Monitor 存在严重告警：%s%s；运行 moox-cli doctor alerts 查看详情", strings.Join(shown, "；"), suffix),
		nil, "inspect_health_check_input")
}

func (r *bootstrapRunner) alertAppliesToNode(object *monitorpb.HealthObject) bool {
	if object == nil {
		return true
	}
	if spaceID := strings.TrimSpace(object.GetSpaceId()); spaceID != "" && spaceID != events.SystemSpaceID {
		return false
	}
	if hostID := strings.TrimSpace(object.GetHostId()); hostID != "" {
		return hostID == r.options.NodeID
	}
	if componentID := strings.TrimSpace(object.GetComponentId()); componentID != "" {
		if r.loadErr != nil {
			return true
		}
		deployment, exists := r.deployments[componentID]
		return exists && deployment.Status == "enabled"
	}
	return true
}

func (r *bootstrapRunner) componentForCheck(id string) (core.Component, string, bool) {
	for _, component := range r.manifest.Components {
		suffix := ":" + component.ComponentID + "@" + r.options.NodeID
		for _, kind := range []string{"service_identity", "network", "path_permissions", "service_autostart"} {
			if id == "bootstrap."+kind+suffix {
				return component, kind, true
			}
		}
		if id == "service.health"+suffix {
			return component, "health", true
		}
		if id == "monitor.component"+suffix {
			return component, "monitor_component", true
		}
	}
	return core.Component{}, "", false
}

// probe 请求 --node 所指节点上组件的健康端口（远端节点经 SSH 转发）。
func (r *bootstrapRunner) probe(ctx context.Context, component core.Component, path string) (ProbeResult, error) {
	if r.options.Probe == nil {
		return ProbeResult{}, fmt.Errorf("健康端口探测器不可用")
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
			return fmt.Errorf("未知的自检项 %q", id)
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
// 不一致说明节点上的组件和这个 CLI 不是同一版本，按 CLI 的目录自检会得出错误的结论。
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
