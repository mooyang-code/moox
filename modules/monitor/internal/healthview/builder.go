package healthview

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/placement"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

const (
	// probeHistoryLimit 是组件详情展示的最近探测次数。
	probeHistoryLimit = 5
	// statusWindow 是推算组件当前状态开始时间时回看的探测次数。
	statusWindow = 20
	// alertResultWindow 是为告警寻找最近一次失败结果时回看的结果数：告警恢复需要连续成功，这期间最新的结果可能已经成功。
	alertResultWindow = 10
	// maxAlerts 是概览列出的告警上限。
	maxAlerts = 200
)

// FactsSource 生成观测事实（observability.Builder）。
type FactsSource interface {
	Build(ctx context.Context, spaceID string) (observability.Overview, error)
}

// PlacementLister 读取 SysDeploy 的部署列表。
type PlacementLister interface {
	Placements(context.Context) ([]*adminpb.DeployPlacement, error)
}

// AgentLister 列出主机采集器（hostmetrics.Store）。
type AgentLister interface {
	ListAgents(context.Context) ([]hostmetrics.AgentView, error)
}

// Builder 生成健康概览 v2。
type Builder struct {
	Facts         FactsSource
	Placements    PlacementLister
	Hosts         AgentLister
	Checks        *store.CheckRepository
	Results       *store.ResultRepository
	Alerts        *store.AlertRepository
	Notifications *store.NotificationRepository
	// Catalog 为空时使用内置的组件目录。
	Catalog *servicecatalog.Catalog
	Now     func() time.Time
}

// Build 生成健康概览；spaceID 为空时包含全部空间。
func (b Builder) Build(ctx context.Context, spaceID string) (Overview, error) {
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	catalog := b.Catalog
	if catalog == nil {
		catalog = servicecatalog.Default()
	}
	out := Overview{GeneratedAt: now}
	var facts observability.Overview
	if b.Facts != nil {
		built, err := b.Facts.Build(ctx, spaceID)
		if err != nil {
			return Overview{}, err
		}
		facts = built
	}
	if facts.GatewayHostsErr != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf("暂时读不到 SysDeploy 的主机列表（%v），主机网关状态不是最新的", facts.GatewayHostsErr))
	}
	var agents []hostmetrics.AgentView
	if b.Hosts != nil {
		listed, err := b.Hosts.ListAgents(ctx)
		if err != nil {
			return Overview{}, err
		}
		agents = listed
	}
	components, warning, err := b.buildComponents(ctx, catalog, facts)
	if err != nil {
		return Overview{}, err
	}
	if warning != "" {
		out.Warnings = append(out.Warnings, warning)
	}
	out.Components = components
	if out.Alerts, err = b.buildAlerts(ctx, spaceID, agents); err != nil {
		return Overview{}, err
	}
	out.Pipeline = buildPipeline(facts, out.Alerts)
	out.BusinessChecks = buildBusinessChecks(facts.BusinessChecks)
	out.Hosts = buildHosts(facts.GatewayHosts, facts.GatewayHostsErr == nil, agents, out.Alerts)
	out.Unregistered = buildUnregistered(facts.Unregistered)
	if out.Notification, err = b.buildNotification(ctx); err != nil {
		return Overview{}, err
	}
	out.Summary = summarize(out)
	return out, nil
}

// deployment 是一条部署：来自 SysDeploy，读不到时取自部署检查。
type deployment struct {
	hostID, componentID string
	enabled             bool
	changedAt           time.Time
}

func (b Builder) buildComponents(ctx context.Context, catalog *servicecatalog.Catalog, facts observability.Overview) ([]Component, string, error) {
	checks := map[string]domain.Check{}
	if b.Checks != nil {
		rows, err := b.Checks.ListBySource(ctx, domain.CheckSourcePlacement)
		if err != nil {
			return nil, "", err
		}
		for _, row := range rows {
			checks[row.CheckID] = row
		}
	}
	deployments, warning := b.deployments(ctx, checks)
	disabledHosts := make(map[string]bool, len(facts.GatewayHosts))
	for _, host := range facts.GatewayHosts {
		disabledHosts[host.HostID] = !host.Enabled
	}
	reporters := latestReporters(facts.Services)
	out := make([]Component, 0, len(deployments))
	for _, item := range deployments {
		component, err := b.component(ctx, catalog, item, checks, disabledHosts[item.hostID], reporters)
		if err != nil {
			return nil, "", err
		}
		out = append(out, component)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].HostID != out[j].HostID {
			return out[i].HostID < out[j].HostID
		}
		return out[i].ComponentID < out[j].ComponentID
	})
	return out, warning, nil
}

// deployments 返回全部部署：优先读 SysDeploy；读不到时退回到部署检查，这时不探测的组件没有检查，不会列出。
func (b Builder) deployments(ctx context.Context, checks map[string]domain.Check) ([]deployment, string) {
	warning := ""
	if b.Placements != nil {
		placements, err := b.Placements.Placements(ctx)
		if err == nil {
			out := make([]deployment, 0, len(placements))
			for _, item := range placements {
				if item == nil {
					continue
				}
				out = append(out, deployment{
					hostID: item.GetHostId(), componentID: item.GetComponentId(),
					enabled: item.GetStatus() == placement.StatusEnabled, changedAt: parseTime(item.GetUpdatedAt()),
				})
			}
			return out, ""
		}
		warning = fmt.Sprintf("暂时读不到 SysDeploy 的部署列表（%v），组件列表取自健康检查，未列出不探测的组件", err)
	}
	out := make([]deployment, 0, len(checks))
	for _, check := range checks {
		hostID, componentID, ok := placement.ParseCheckID(check.CheckID)
		if !ok {
			continue
		}
		out = append(out, deployment{hostID: hostID, componentID: componentID, enabled: check.Enabled, changedAt: check.UpdatedAt.UTC()})
	}
	return out, warning
}

func (b Builder) component(
	ctx context.Context,
	catalog *servicecatalog.Catalog,
	item deployment,
	checks map[string]domain.Check,
	hostDisabled bool,
	reporters map[string]observability.ServiceStatus,
) (Component, error) {
	out := Component{HostID: item.hostID, ComponentID: item.componentID, Name: item.componentID}
	definition, known := catalog.Component(item.componentID)
	if !known {
		out.Status, out.Reason = StatusUnknown, "组件目录中没有这个组件"
		return out, nil
	}
	out.Name = definition.Name
	check, hasCheck := checks[placement.CheckID(item.hostID, item.componentID)]
	if hasCheck {
		out.Probe.URL = check.URL
	}
	switch {
	case hostDisabled:
		out.Status, out.Reason = StatusDisabled, "主机已停用"
		return out, nil
	case !item.enabled:
		out.Status, out.Reason, out.StatusSince = StatusDisabled, "部署已停用", item.changedAt
		return out, nil
	}
	var results []domain.CheckResult
	switch {
	case definition.Health.Kind == servicecatalog.HealthNone:
		out.Probe.Status = StatusUnchecked
	case !hasCheck || b.Results == nil:
		out.Probe.Status = StatusUnknown
	default:
		recent, err := b.Results.Recent(ctx, check.SpaceID, check.CheckID, statusWindow)
		if err != nil {
			return Component{}, err
		}
		results = recent
		out.Probe.Status = StatusUnknown
		for index, result := range results {
			probe := probeOf(result, check.URL)
			if index == 0 {
				out.Probe = probe
			}
			if index < probeHistoryLimit {
				out.History = append(out.History, probe)
			}
		}
	}
	reporterStatus := ""
	if definition.Observability.Transport == servicecatalog.TransportReporter {
		out.Reporter = Reporter{Status: observability.ReporterNeverReported}
		if service, ok := reporters[reporterKey(item.hostID, item.componentID)]; ok {
			out.Reporter = Reporter{
				Status: service.ReporterStatus, InstanceID: service.InstanceID,
				Version: service.Version, LastSeenAt: service.ReportedAt,
			}
		}
		reporterStatus = reporterHealth(out.Reporter.Status)
	}
	out.Status = worst(out.Probe.Status, reporterStatus)
	out.Reason, out.StatusSince = componentReason(out, results)
	return out, nil
}

// probeOf 把一次检查结果转成探测结果。
func probeOf(result domain.CheckResult, url string) Probe {
	status := StatusHealthy
	switch {
	case !result.Success:
		status = StatusDown
	case result.Status == domain.CheckStatusDegraded:
		status = StatusDegraded
	}
	return Probe{Status: status, URL: url, CheckedAt: result.CheckedAt.UTC(), RawError: strings.TrimSpace(result.ErrorMessage)}
}

func reporterKey(hostID, componentID string) string {
	return hostID + "\x00" + componentID
}

// latestReporters 为每个部署挑出最近上报的实例：组件重启后实例 ID 改变，旧实例会在目录里停留到清理为止。
func latestReporters(services []observability.ServiceStatus) map[string]observability.ServiceStatus {
	out := make(map[string]observability.ServiceStatus)
	for _, service := range services {
		if service.ReporterStatus == "" {
			continue
		}
		key := reporterKey(service.NodeID, service.ServiceName)
		if current, ok := out[key]; !ok || service.ReportedAt.After(current.ReportedAt) {
			out[key] = service
		}
	}
	return out
}

// reporterHealth 把上报状态折算成组件状态：上报中断或从未上报都算降级。
func reporterHealth(status string) string {
	switch status {
	case "healthy":
		return StatusHealthy
	case "stale", observability.ReporterNeverReported:
		return StatusDegraded
	default:
		return StatusUnknown
	}
}

// componentReason 返回组件状态的中文原因和当前状态的开始时间：状态由探测决定时，开始时间是最近一段同状态探测中
// 最早的一次（最多回看 statusWindow 次）；由上报中断决定时，是最近一次上报的时间。
func componentReason(component Component, results []domain.CheckResult) (string, time.Time) {
	if component.Status == StatusUnchecked {
		return "不探测", time.Time{}
	}
	if component.Probe.Status == component.Status {
		var since time.Time
		for _, result := range results {
			if probeOf(result, "").Status != component.Status {
				break
			}
			since = result.CheckedAt.UTC()
		}
		switch component.Status {
		case StatusDown:
			return probeReason(component.Probe.RawError), since
		case StatusDegraded:
			return "健康检查报告服务降级", since
		case StatusUnknown:
			return "尚未完成健康检查", since
		default:
			return "运行正常", since
		}
	}
	switch component.Reporter.Status {
	case "healthy":
		return "运行正常", time.Time{}
	case "stale":
		return "运行指标上报已中断，最近上报 " + alerttext.Time(component.Reporter.LastSeenAt), component.Reporter.LastSeenAt
	case observability.ReporterNeverReported:
		return "从未上报运行指标", time.Time{}
	default:
		return "运行指标上报状态未知", time.Time{}
	}
}

func (b Builder) buildAlerts(ctx context.Context, spaceID string, agents []hostmetrics.AgentView) ([]Alert, error) {
	out := []Alert{}
	if b.Alerts == nil {
		return out, nil
	}
	states, err := b.Alerts.ListEnabledFiringStates(ctx, spaceID, maxAlerts)
	if err != nil {
		return nil, err
	}
	agentsByID := make(map[string]hostmetrics.AgentView, len(agents))
	for _, agent := range agents {
		agentsByID[agent.AgentID] = agent
	}
	for _, state := range states {
		alert := Alert{ID: state.DedupeKey, Severity: SeverityCritical, LastCheckedAt: state.UpdatedAt.UTC()}
		if alert.ID == "" {
			alert.ID = state.RuleID + ":" + state.CheckID
		}
		if state.TriggeredAt != nil {
			alert.TriggeredAt = state.TriggeredAt.UTC()
		}
		if agentID, metric, ok := hostmetrics.ParseHostRuleKey(state.CheckID); ok {
			err = b.describeHostAlert(ctx, &alert, state, agentsByID[agentID], agentID, metric)
		} else {
			err = b.describeCheckAlert(ctx, &alert, state)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, alert)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == SeverityCritical
		}
		return out[i].TriggeredAt.After(out[j].TriggeredAt)
	})
	return out, nil
}

// describeHostAlert 填写主机资源阈值告警：主机按主机采集器上报的主机 ID 对应，原因给出当前值和阈值。
func (b Builder) describeHostAlert(ctx context.Context, alert *Alert, state domain.AlertState, agent hostmetrics.AgentView, agentID, metric string) error {
	host := firstNonEmpty(agent.HostID, agent.Hostname, agentID)
	alert.Severity = SeverityWarning
	alert.Target = Target{Kind: TargetHost, HostID: host}
	alert.Title = "主机 " + host + " · " + hostmetrics.MetricLabel(metric)
	var rule domain.AlertRule
	if found, err := b.Alerts.GetRule(ctx, state.SpaceID, state.RuleID); err == nil {
		rule = *found
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	alert.Reason = hostmetrics.HostAlertReason(rule, metric, agent.Snapshot)
	if seen := parseTime(agent.LastSeenAt); !seen.IsZero() {
		alert.LastCheckedAt = seen
	}
	return nil
}

// describeCheckAlert 填写检查告警：标题取检查名称，原因取最近一次失败结果。
func (b Builder) describeCheckAlert(ctx context.Context, alert *Alert, state domain.AlertState) error {
	alert.Title = state.CheckID
	if b.Checks != nil {
		check, err := b.Checks.Get(ctx, state.SpaceID, state.CheckID)
		switch {
		case err == nil:
			if check.Name != "" {
				alert.Title = check.Name
			}
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}
	}
	alert.Target, alert.Stage = alertTarget(state.SpaceID, state.CheckID)
	var failure domain.CheckResult
	if b.Results != nil {
		results, err := b.Results.Recent(ctx, state.SpaceID, state.CheckID, alertResultWindow)
		if err != nil {
			return err
		}
		if len(results) > 0 {
			alert.LastCheckedAt = results[0].CheckedAt.UTC()
			failure = results[0]
			for _, result := range results {
				if !result.Success {
					failure = result
					break
				}
			}
		}
	}
	raw := strings.TrimSpace(failure.ErrorMessage)
	switch {
	case alert.Target.Kind == TargetComponent && strings.HasPrefix(state.CheckID, "placement:"):
		alert.Reason = probeReason(raw)
		alert.RawError = rawError(raw, alert.Reason)
	case raw == "":
		alert.Reason = "监控项持续异常"
		alert.RawError = strings.TrimSpace(failure.BodyExcerpt)
	default:
		alert.Reason = ChineseReason(alerttext.Reason(raw))
		alert.RawError = rawError(raw, alert.Reason)
		if alert.RawError == "" {
			// 业务检查的结果已经是中文原因，机器诊断信息放在 BodyExcerpt 里。
			alert.RawError = strings.TrimSpace(failure.BodyExcerpt)
		}
	}
	return nil
}

// alertTarget 按检查 ID 的前缀确定告警对象，以及它所属的数据链路阶段（不属于数据链路时为空）。
func alertTarget(spaceID, checkID string) (Target, string) {
	if hostID, componentID, ok := placement.ParseCheckID(checkID); ok {
		return Target{Kind: TargetComponent, HostID: hostID, ComponentID: componentID}, ""
	}
	prefix, rest, _ := strings.Cut(checkID, ":")
	switch prefix {
	case "reporter":
		// reporter:<主机>:<组件>:<实例>
		if parts := strings.SplitN(rest, ":", 3); len(parts) >= 2 {
			return Target{Kind: TargetComponent, HostID: parts[0], ComponentID: parts[1]}, ""
		}
	case "host_gateway":
		return Target{Kind: TargetHost, HostID: rest}, ""
	case "dataset":
		if producer, datasetID, freq, ok := datasetCheckParts(checkID); ok {
			return Target{Kind: TargetDataset, SpaceID: spaceID, DatasetID: datasetID, Frequency: freq}, datasetStage(producer)
		}
	case "kline_freshness":
		// kline_freshness:<空间>:<视图>:<频率>；采集任务清单（kline_freshness:task_result_inventory）是业务检查。
		if parts := strings.Split(rest, ":"); len(parts) == 3 {
			return Target{Kind: TargetDataset, SpaceID: parts[0], DatasetID: parts[1], Frequency: parts[2]}, StageCollect
		}
		return Target{Kind: TargetBusiness, SpaceID: spaceID}, StageCollect
	case "market_fetch", "market_canary", "subject_tag":
		return Target{Kind: TargetBusiness, SpaceID: spaceID}, StageCollect
	case "data_delivery":
		return Target{Kind: TargetBusiness, SpaceID: spaceID}, StageStorage
	case "balance":
		return Target{Kind: TargetBusiness, SpaceID: spaceID}, StageTrade
	}
	return Target{Kind: TargetBusiness, SpaceID: spaceID}, ""
}

// pipelineStages 是数据链路的阶段，按数据流向排列。
var pipelineStages = []struct{ id, name string }{
	{StageCollect, "采集"},
	{StageStorage, "存储与视图"},
	{StageFactor, "因子"},
	{StageTrade, "交易"},
}

// datasetStage 按数据集的生产者确定数据链路阶段。
func datasetStage(producer string) string {
	switch strings.ToLower(strings.TrimSpace(producer)) {
	case "collector":
		return StageCollect
	case "factor":
		return StageFactor
	case "trade", "strategy":
		return StageTrade
	default:
		// storage、storage_view 以及其他数据处理模块。
		return StageStorage
	}
}

// businessStage 按业务检查的类型确定数据链路阶段。
func businessStage(kind string) string {
	switch kind {
	case "market_fetch", "canary":
		return StageCollect
	case "data_delivery":
		return StageStorage
	case "balance":
		return StageTrade
	default:
		return ""
	}
}

// buildPipeline 生成数据链路：阶段状态取该阶段数据集、业务检查和告警中最严重的一个，没有任何数据时为「不探测」。
func buildPipeline(facts observability.Overview, alerts []Alert) []PipelineStage {
	storageScopes := make(map[string]struct{})
	for _, item := range facts.Datasets {
		if strings.EqualFold(strings.TrimSpace(item.Producer), "storage") {
			storageScopes[datasetScopeKey(item.SpaceID, item.DatasetID, item.Freq)] = struct{}{}
		}
	}
	datasets := make(map[string][]PipelineDataset, len(pipelineStages))
	statuses := make(map[string][]string, len(pipelineStages))
	for _, item := range facts.Datasets {
		if isHostMonitoringDataset(item) || collectorCoveredByStorage(item, storageScopes) {
			continue
		}
		status := normalizeStatus(item.Status)
		reason, raw := datasetReason(item)
		stage := datasetStage(item.Producer)
		datasets[stage] = append(datasets[stage], PipelineDataset{
			SpaceID: item.SpaceID, DatasetID: item.DatasetID, Frequency: item.Freq, Producer: item.Producer,
			Status: status, WatermarkAt: item.OutputWatermarkAt.UTC(), LagSeconds: item.LagSeconds,
			LastSuccessAt: item.LastSuccessAt.UTC(), Reason: reason, RawError: raw,
		})
		statuses[stage] = append(statuses[stage], status)
	}
	for _, item := range facts.BusinessChecks {
		if stage := businessStage(item.Kind); stage != "" {
			statuses[stage] = append(statuses[stage], normalizeStatus(item.Status))
		}
	}
	for _, alert := range alerts {
		if alert.Stage != "" {
			statuses[alert.Stage] = append(statuses[alert.Stage], StatusDown)
		}
	}
	out := make([]PipelineStage, 0, len(pipelineStages))
	for _, definition := range pipelineStages {
		stage := PipelineStage{Stage: definition.id, Name: definition.name, Status: StatusUnchecked, Datasets: datasets[definition.id]}
		if len(statuses[definition.id]) > 0 {
			stage.Status = worst(statuses[definition.id]...)
		}
		if stage.Datasets == nil {
			stage.Datasets = []PipelineDataset{}
		}
		sort.Slice(stage.Datasets, func(i, j int) bool {
			left, right := stage.Datasets[i], stage.Datasets[j]
			if statusRank(left.Status) != statusRank(right.Status) {
				return statusRank(left.Status) > statusRank(right.Status)
			}
			return datasetScopeKey(left.SpaceID, left.DatasetID, left.Frequency)+left.Producer <
				datasetScopeKey(right.SpaceID, right.DatasetID, right.Frequency)+right.Producer
		})
		out = append(out, stage)
	}
	return out
}

// buildBusinessChecks 生成业务检查列表，异常的排在前面。
func buildBusinessChecks(items []observability.BusinessStatus) []BusinessCheck {
	out := make([]BusinessCheck, 0, len(items))
	for _, item := range items {
		status, reason := normalizeStatus(item.Status), ChineseReason(item.Reason)
		check := BusinessCheck{
			Kind: item.Kind, Name: firstNonEmpty(item.Name, item.Kind+" "+item.Module), Module: item.Module,
			SpaceID: item.SpaceID, Status: status, Reason: reason, Stage: businessStage(item.Kind),
			CheckedAt: item.LastCheckedAt.UTC(),
		}
		if status != StatusHealthy {
			check.RawError = rawError(item.Reason, reason)
		}
		out = append(out, check)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if statusRank(out[i].Status) != statusRank(out[j].Status) {
			return statusRank(out[i].Status) > statusRank(out[j].Status)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// buildHosts 生成主机摘要：主机来自 SysDeploy，资源占用来自主机采集器（按上报的主机 ID 对应）。gatewayKnown 为 false
// 表示 SysDeploy 暂时读不到，这时只列出在上报快照的主机。
func buildHosts(gateways []observability.GatewayHostStatus, gatewayKnown bool, agents []hostmetrics.AgentView, alerts []Alert) []Host {
	agentByHost := make(map[string]hostmetrics.AgentView, len(agents))
	for _, agent := range agents {
		if agent.HostID == "" {
			continue
		}
		if current, ok := agentByHost[agent.HostID]; !ok || parseTime(agent.LastSeenAt).After(parseTime(current.LastSeenAt)) {
			agentByHost[agent.HostID] = agent
		}
	}
	thresholds := make(map[string][]string)
	for _, alert := range alerts {
		if alert.Target.Kind == TargetHost && alert.Severity == SeverityWarning {
			thresholds[alert.Target.HostID] = append(thresholds[alert.Target.HostID], alert.Reason)
		}
	}
	out := make([]Host, 0, len(gateways)+len(agentByHost))
	listed := make(map[string]bool, len(gateways))
	for _, gateway := range gateways {
		listed[gateway.HostID] = true
		agent, hasAgent := agentByHost[gateway.HostID]
		host := hostSummary(gateway.HostID, agent, hasAgent)
		host.GatewayState = gateway.GatewayState
		if !gateway.Enabled {
			host.Status, host.Reason = StatusDisabled, "主机已停用"
		} else {
			gatewayStatus := StatusHealthy
			if !gateway.Healthy {
				gatewayStatus = StatusDegraded
				if gateway.GatewayState == "offline" || gateway.GatewayState == "never_reported" {
					gatewayStatus = StatusDown
				}
			}
			host.Status, host.Reason = hostStatus(
				statusReason{gatewayStatus, gateway.Reason},
				agentStatus(agent, hasAgent),
				thresholdStatus(thresholds[gateway.HostID]),
			)
		}
		out = append(out, host)
	}
	for hostID, agent := range agentByHost {
		if listed[hostID] {
			continue
		}
		host := hostSummary(hostID, agent, true)
		registration := statusReason{StatusDegraded, "主机没有在 SysDeploy 登记，请在 moox.toml 的部署表中登记"}
		if !gatewayKnown {
			registration = statusReason{}
		}
		host.Status, host.Reason = hostStatus(registration, agentStatus(agent, true), thresholdStatus(thresholds[hostID]))
		out = append(out, host)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostID < out[j].HostID })
	return out
}

// statusReason 是一个来源给出的状态和原因。
type statusReason struct{ status, reason string }

// hostStatus 取各来源中最严重的状态；都正常时为「运行正常」。
func hostStatus(sources ...statusReason) (string, string) {
	result := statusReason{StatusHealthy, "运行正常"}
	for _, source := range sources {
		if source.status != "" && source.status != StatusHealthy && statusRank(source.status) > statusRank(result.status) {
			result = source
		}
	}
	return result.status, result.reason
}

func agentStatus(agent hostmetrics.AgentView, hasAgent bool) statusReason {
	switch {
	case !hasAgent:
		return statusReason{StatusUnknown, "尚未收到主机采集器的快照"}
	case !agent.Reachable:
		return statusReason{StatusDown, "主机采集器失联，最近上报 " + alerttext.Time(parseTime(agent.LastSeenAt))}
	default:
		return statusReason{StatusHealthy, ""}
	}
}

func thresholdStatus(reasons []string) statusReason {
	if len(reasons) == 0 {
		return statusReason{}
	}
	return statusReason{StatusDegraded, strings.Join(reasons, "；")}
}

// hostSummary 填写主机采集器上报的资源占用：磁盘取占用最高的文件系统。
func hostSummary(hostID string, agent hostmetrics.AgentView, hasAgent bool) Host {
	host := Host{HostID: hostID}
	if !hasAgent {
		return host
	}
	host.AgentID = agent.AgentID
	host.LastSeenAt = parseTime(agent.LastSeenAt)
	if snapshot := agent.Snapshot; snapshot != nil {
		if cpu := snapshot.GetCpu(); cpu != nil && cpu.GetUsageAvailable() {
			host.CPUPercent = cpu.GetUsagePercent()
		}
		host.MemoryPercent = snapshot.GetMemory().GetUsagePercent()
		for _, filesystem := range snapshot.GetFilesystems() {
			host.DiskPercent = max(host.DiskPercent, filesystem.GetUsagePercent())
		}
	}
	return host
}

func buildUnregistered(items []monmetrics.UnregisteredProducer) []Unregistered {
	out := make([]Unregistered, 0, len(items))
	for _, item := range items {
		out = append(out, Unregistered{
			HostID: item.NodeID, ComponentID: item.ServiceName, InstanceID: item.InstanceID,
			Version: item.Version, LastSeenAt: item.LastSeenAt.UTC(),
		})
	}
	return out
}

func (b Builder) buildNotification(ctx context.Context) (Notification, error) {
	if b.Notifications == nil {
		return Notification{}, nil
	}
	channel, err := b.Notifications.GetGlobal(ctx)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Notification{}, nil
	}
	if err != nil {
		return Notification{}, err
	}
	return Notification{
		ChannelType: channel.ChannelType, Configured: strings.TrimSpace(channel.WebhookURL) != "",
		WebhookMasked: MaskURL(channel.WebhookURL),
	}, nil
}

// summarize 统计组件、主机、业务检查和数据集：降级和故障算「需关注」；停用和不探测的不计入。
func summarize(overview Overview) Summary {
	summary := Summary{Alerts: len(overview.Alerts)}
	count := func(status string) {
		switch status {
		case StatusDown, StatusDegraded:
			summary.Attention++
		case StatusHealthy:
			summary.Healthy++
		case StatusUnknown:
			summary.Unknown++
		}
	}
	for _, item := range overview.Components {
		count(item.Status)
	}
	for _, item := range overview.Hosts {
		count(item.Status)
	}
	for _, item := range overview.BusinessChecks {
		count(item.Status)
	}
	for _, stage := range overview.Pipeline {
		for _, item := range stage.Datasets {
			count(item.Status)
		}
	}
	return summary
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
