package healthview

import (
	"sort"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func projectHosts(facts observability.Overview) []*pb.HealthHost {
	byID := make(map[string]domain.TopologyHost)
	expectedAgent := make(map[string]bool)
	agentDisabled := make(map[string]bool)
	if facts.Topology != nil {
		for _, host := range facts.Topology.Hosts {
			byID[host.HostID] = host
		}
		for _, deployment := range facts.Topology.Deployments {
			if deployment.ComponentID == "host-agent" && deployment.Status == "disabled" {
				agentDisabled[deployment.HostID] = true
			}
			if deployment.ComponentID == "host-agent" && deployment.Status == "enabled" {
				expectedAgent[deployment.HostID] = true
			}
		}
	}
	// reported 表示这台主机有过 host-agent 上报。
	decorate := func(host *pb.HealthHost, reported bool) {
		if agentDisabled[host.HostId] {
			host.Status, host.Reason = "unknown", "主机资源采集已停用，等待网关状态"
		}
		if registered, ok := byID[host.HostId]; ok {
			host.Address = registered.Address
			if registered.Status == "disabled" {
				host.Status, host.Reason = "disabled", "主机已停用"
			}
		}
		for _, signal := range facts.GatewaySignals {
			if signal.HostID != host.HostId {
				continue
			}
			host.GatewaySignals = append(host.GatewaySignals, &pb.HealthGatewaySignal{Kind: signal.Kind, PendingSince: stamp(signal.PendingSince), ExpectedHash: signal.ExpectedHash, AppliedHash: signal.AppliedHash, Signal: &pb.HealthSignal{Status: domain.HealthStatus(signal.Status), Reason: signal.Reason, RawError: signal.RawError, CheckedAt: stamp(signal.CheckedAt)}})
			if host.Status == "disabled" {
				continue
			}
			if signal.Kind == "heartbeat" && (!reported || agentDisabled[host.HostId]) && !expectedAgent[host.HostId] {
				host.Status, host.Reason = domain.HealthStatus(signal.Status), signal.Reason
			}
			// 已按主机失联显示的主机保留失联的原因（与告警同一段文字），不被单项网关信号的说明盖掉。
			if signal.Status == "down" && host.Status != "down" {
				host.Status, host.Reason = "down", signal.Reason
			}
		}
	}
	var out []*pb.HealthHost
	covered := make(map[string]bool)
	// 主机状态取在线情形的结论（与主机类告警同一处判断）：只有 host-agent 停报时主机仍在线，显示需关注而不是异常。
	statuses := map[string]string{observability.HostOnline: "healthy", observability.HostAgentStopped: "degraded", observability.HostLost: "down"}
	for _, presence := range facts.HostPresences() {
		item := presence.Host
		host := &pb.HealthHost{HostId: item.HostID, Hostname: item.Hostname, Status: statuses[presence.State], Reason: presence.Reason, CpuPercent: item.CPUPercent, MemoryPercent: item.MemoryPercent, DiskPercent: item.FilesystemMaxPercent, MemoryAvailable: item.MemoryAvailable, DiskAvailable: item.DiskAvailable, MetricsAvailable: item.MetricsAvailable, CpuAvailable: item.CPUAvailable, LastReportedAt: stamp(item.LastSeenAt),
			CpuCores: item.CPUCores, MemoryTotalBytes: item.MemoryTotalBytes, MemoryUsedBytes: item.MemoryUsedBytes,
			DiskTotalBytes: item.FilesystemTotalBytes, DiskUsedBytes: item.FilesystemUsedBytes, DiskMountpoint: item.FilesystemMountpoint,
			// 告警阈值，页面据此画出阈值刻度。
			CpuThreshold:    hostmetrics.MetricThreshold(hostmetrics.HostMetricCPU).Alert,
			MemoryThreshold: hostmetrics.MetricThreshold(hostmetrics.HostMetricMemory).Alert,
			DiskThreshold:   hostmetrics.MetricThreshold(hostmetrics.HostMetricFilesystemUsage).Alert}
		decorate(host, true)
		out = append(out, host)
		covered[item.HostID] = true
	}
	if facts.Topology != nil {
		for _, item := range facts.Topology.Hosts {
			if covered[item.HostID] {
				continue
			}
			host := &pb.HealthHost{HostId: item.HostID, Status: "unknown", Reason: "尚未收到主机指标"}
			decorate(host, false)
			out = append(out, host)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostId < out[j].HostId })
	return out
}
