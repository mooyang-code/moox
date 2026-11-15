package observability

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

type ReporterInstance struct {
	InstanceID, BootID, Version, Status string
	LastSeenAt                          time.Time
}

// 组件的上报来源（设计 3.8）：组件自己经 packages/report 上报；host-agent 的主机快照算作它的上报；
// 因子引擎与归档 worker 由管理端按心跳代报。
const (
	SourceReporter     = "reporter"
	SourceHostSnapshot = "host_snapshot"
	SourceDelegated    = "delegated"
)

// buildServices 把每个登记的部署和它的上报对上：按主机加组件 ID 精确匹配；管理端代报的外部程序不带主机，
// 按组件名对到这个组件唯一的部署上。组件健康只看上报（设计 3.8）：有上报且就绪为正常，上报了未就绪为未就绪，
// 超过无数据时限没有上报为停止上报，从没上报过也算停止上报。
func (b Builder) buildServices(ctx context.Context, now time.Time, hosts []HostStatus) ([]ServiceStatus, []ServiceStatus, *domain.TopologySnapshot, error) {
	if b.Topology == nil {
		return nil, nil, nil, nil
	}
	snapshot, err := b.Topology.Snapshot(ctx)
	if err != nil || snapshot == nil {
		return nil, nil, nil, err
	}
	rows, err := b.reporters(ctx, now)
	if err != nil {
		return nil, nil, snapshot, err
	}
	boots, err := b.bootChanges(ctx, now)
	if err != nil {
		return nil, nil, snapshot, err
	}
	byDeployment := make(map[string][]monmetrics.MetricService)
	for _, row := range rows {
		key := deploymentKey(row.NodeID, row.ServiceName)
		byDeployment[key] = append(byDeployment[key], row)
	}
	topologyHosts := make(map[string]domain.TopologyHost, len(snapshot.Hosts))
	for _, host := range snapshot.Hosts {
		topologyHosts[host.HostID] = host
	}
	registered := make(map[string]bool, len(snapshot.Deployments))
	services := make([]ServiceStatus, 0, len(snapshot.Deployments))
	for _, deployment := range snapshot.Deployments {
		key := deploymentKey(deployment.HostID, deployment.ComponentID)
		registered[key] = true
		component, _ := snapshot.Catalog.Component(deployment.ComponentID)
		item := ServiceStatus{
			NodeID: deployment.HostID, ServiceName: deployment.ComponentID, Source: componentSource(component),
			Enabled: deployment.Status == servicecatalog.Enabled && topologyHosts[deployment.HostID].Status == servicecatalog.Enabled,
		}
		switch item.Source {
		case SourceHostSnapshot:
			item = withHostSnapshot(item, hosts)
		case SourceDelegated:
			item = withReporters(item, byDeployment[deploymentKey("", deployment.ComponentID)])
			item.BootChanges = boots[deploymentKey("", deployment.ComponentID)]
		default:
			item = withReporters(item, byDeployment[key])
			item.BootChanges = boots[key]
		}
		if !item.Enabled {
			item.Status, item.Reason = "disabled", "部署已停用"
		}
		services = append(services, item)
	}
	// Catalog principals run outside host deployments (for example SCF). Their
	// metrics remain available to business checks without inventing deployments.
	external := make(map[string]bool)
	for _, principal := range snapshot.Catalog.ExternalIdentities() {
		external[principal] = true
	}
	var unregistered []ServiceStatus
	for _, row := range rows {
		if registered[deploymentKey(row.NodeID, row.ServiceName)] || external[row.ServiceName] || row.IsStale || row.LastSeenAt.IsZero() {
			continue
		}
		item := withReporters(ServiceStatus{NodeID: row.NodeID, ServiceName: row.ServiceName, Source: SourceReporter}, []monmetrics.MetricService{row})
		item.Status, item.Reason = "unregistered", "进程在上报，但没有部署记录"
		unregistered = append(unregistered, item)
	}
	sort.Slice(unregistered, func(i, j int) bool {
		a, z := unregistered[i], unregistered[j]
		return strings.Join([]string{a.NodeID, a.ServiceName, a.InstanceID}, "\x00") < strings.Join([]string{z.NodeID, z.ServiceName, z.InstanceID}, "\x00")
	})
	return services, unregistered, snapshot, nil
}

// componentSource 按目录里组件的上报方式（doctor.transport）给出它健康的来源。
func componentSource(component servicecatalog.Component) string {
	switch component.Doctor.Transport {
	case SourceHostSnapshot, SourceDelegated:
		return component.Doctor.Transport
	}
	return SourceReporter
}

func (b Builder) reporters(ctx context.Context, now time.Time) ([]monmetrics.MetricService, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	rows, total, err := b.Metrics.Catalog().ListServicesAt(ctx, "", 0, 500, now)
	if err != nil {
		return nil, err
	}
	if total > maxOverviewServices {
		return nil, fmt.Errorf("observability services exceed limit %d", maxOverviewServices)
	}
	if total > int64(len(rows)) {
		more, _, err := b.Metrics.Catalog().ListServicesAt(ctx, "", len(rows), int(total)-len(rows), now)
		if err != nil {
			return nil, err
		}
		rows = append(rows, more...)
	}
	return rows, nil
}

func (b Builder) bootChanges(ctx context.Context, now time.Time) (map[string]int, error) {
	if b.Metrics == nil || b.Metrics.Catalog() == nil {
		return nil, nil
	}
	return b.Metrics.Catalog().BootChanges(ctx, now.Add(-monmetrics.RestartWindow))
}

func deploymentKey(hostID, componentID string) string { return hostID + "\x00" + componentID }

// withReporters 按最近一次上报判断组件状态：上报新鲜时看就绪，未就绪记为 down 并带上原因，模块级生命周期也只在上报新鲜时挂上
// （停止上报的模块不能显示成最后的暂停状态）。上报的新鲜度记在 ReporterStatus：missing（从未上报）、stale（停止上报）。
// 已启用的部署从没上报或停止上报，组件状态记为 down：进程挂了与进程活着但未就绪一样严重（告警都是严重），
// setup verify 也据此拦住没起来的组件；未启用的部署由调用方改成 disabled。
func withReporters(item ServiceStatus, rows []monmetrics.MetricService) ServiceStatus {
	item.ReporterStatus, item.Status, item.Reason = "missing", silentStatus(item, "unknown"), "reporter missing"
	var latest *monmetrics.MetricService
	for i, row := range rows {
		status := "healthy"
		if row.IsStale {
			status = "stale"
		}
		item.Instances = append(item.Instances, ReporterInstance{InstanceID: row.InstanceID, BootID: row.BootID, Version: row.Version, Status: status, LastSeenAt: row.LastSeenAt.UTC()})
		if row.LastSeenAt.After(item.LastSeenAt) {
			item.InstanceID, item.LastSeenAt, item.ReporterStatus = row.InstanceID, row.LastSeenAt.UTC(), status
			latest = &rows[i]
		}
	}
	if latest == nil {
		return item
	}
	item.Readiness, item.NotReadyReason = latest.Readiness, latest.NotReadyReason
	if item.ReporterStatus != "healthy" {
		item.Status, item.Reason = silentStatus(item, item.ReporterStatus), "reporter stale"
		return item
	}
	item.Status, item.Reason = "healthy", "reporter fresh"
	if item.Readiness == monmetrics.ReadinessNotReady {
		item.Status, item.Reason = "down", "未就绪："+firstNonEmptyText(item.NotReadyReason, "组件报告未就绪")
	}
	item.Lifecycle = lifecycleFromReport(monmetrics.DecodeLifecycle(latest.LifecycleJSON))
	return item
}

// silentStatus 是没有新鲜上报时的组件状态：已启用的部署记为 down，否则保留 fallback（未启用的部署随后由调用方改成 disabled）。
func silentStatus(item ServiceStatus, fallback string) string {
	if item.Enabled {
		return "down"
	}
	return fallback
}

// withHostSnapshot 用这台主机最近一次主机快照判断 host-agent：快照新鲜为正常，否则停止上报（已启用时状态同样记为 down）。
// 主机失联、采集器停报由主机类检查告警，这里不另起上报检查。
func withHostSnapshot(item ServiceStatus, hosts []HostStatus) ServiceStatus {
	item.ReporterStatus, item.Status, item.Reason = "missing", silentStatus(item, "unknown"), "reporter missing"
	for _, host := range hosts {
		if host.HostID != item.NodeID || !host.LastSeenAt.After(item.LastSeenAt) {
			continue
		}
		item.InstanceID, item.LastSeenAt = host.AgentID, host.LastSeenAt.UTC()
		item.ReporterStatus, item.Status, item.Reason = "healthy", "healthy", "reporter fresh"
		if host.Status != "healthy" {
			item.ReporterStatus, item.Status, item.Reason = "stale", silentStatus(item, "stale"), "reporter stale"
		}
	}
	return item
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
