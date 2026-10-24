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
	decorate := func(host *pb.HealthHost) {
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
			if signal.Kind == "heartbeat" && (host.AgentId == "" || agentDisabled[host.HostId]) && !expectedAgent[host.HostId] {
				host.Status, host.Reason = domain.HealthStatus(signal.Status), signal.Reason
			}
			if signal.Status == "down" {
				host.Status, host.Reason = "down", signal.Reason
			}
		}
	}
	var out []*pb.HealthHost
	covered := make(map[string]bool)
	for _, item := range facts.Hosts {
		if hostRemoved(facts, item.HostID) {
			continue
		}
		host := &pb.HealthHost{HostId: item.HostID, AgentId: item.AgentID, Hostname: item.Hostname, Status: domain.HealthStatus(item.Status), Reason: ChineseReason(item.Reason), CpuPercent: item.CPUPercent, MemoryPercent: item.MemoryPercent, DiskPercent: item.FilesystemMaxPercent, MemoryAvailable: item.MemoryAvailable, DiskAvailable: item.DiskAvailable, MetricsAvailable: item.MetricsAvailable, CpuAvailable: item.CPUAvailable, LastReportedAt: stamp(item.LastSeenAt),
			CpuCores: item.CPUCores, MemoryTotalBytes: item.MemoryTotalBytes, MemoryUsedBytes: item.MemoryUsedBytes,
			DiskTotalBytes: item.FilesystemTotalBytes, DiskUsedBytes: item.FilesystemUsedBytes, DiskMountpoint: item.FilesystemMountpoint}
		decorate(host)
		out = append(out, host)
		covered[item.HostID] = true
	}
	if facts.Topology != nil {
		for _, item := range facts.Topology.Hosts {
			if covered[item.HostID] {
				continue
			}
			host := &pb.HealthHost{HostId: item.HostID, Status: "unknown", Reason: "尚未收到主机指标"}
			decorate(host)
			out = append(out, host)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HostId+"\x00"+out[i].AgentId < out[j].HostId+"\x00"+out[j].AgentId })
	return out
}

// hostRemoved 判断采集代理所在主机是否已从部署拓扑中删除（例如主机改名后旧主机被移除）：这类代理不再上报，
// 不应继续显示为失联。拓扑未知或代理没有主机 ID 时不做判断。
func hostRemoved(facts observability.Overview, hostID string) bool {
	if facts.Topology == nil || len(facts.Topology.Hosts) == 0 || hostID == "" {
		return false
	}
	for _, host := range facts.Topology.Hosts {
		if host.HostID == hostID {
			return false
		}
	}
	return true
}

// Presence notifications are code-owned transitions, not threshold rules in the
// alert repository. Project the same silence cutoff as a current host alert.
func hostPresenceAlerts(facts observability.Overview) []*pb.HealthAlert {
	disabled := make(map[string]bool)
	if facts.Topology != nil {
		for _, host := range facts.Topology.Hosts {
			if host.Status == "disabled" {
				disabled[host.HostID] = true
			}
		}
		for _, deployment := range facts.Topology.Deployments {
			if deployment.ComponentID == "host-agent" && deployment.Status == "disabled" {
				disabled[deployment.HostID] = true
			}
		}
	}
	var out []*pb.HealthAlert
	for _, host := range facts.Hosts {
		if host.Status != "down" || disabled[host.HostID] || hostRemoved(facts, host.HostID) {
			continue
		}
		title := host.HostID
		if title == "" {
			title = host.AgentID
		}
		out = append(out, &pb.HealthAlert{Id: "host-presence:" + host.AgentID, Severity: "critical", Object: &pb.HealthObject{Type: "host", HostId: host.HostID, AgentId: host.AgentID, Kind: "presence"}, Title: "主机 " + title + " · 失联", Reason: "主机采集代理超过三分钟未上报，主机可能宕机、断网，或采集代理已停止", TriggeredAt: stamp(host.LastSeenAt.Add(hostmetrics.DefaultHostStaleAfter)), LastCheckedAt: stamp(facts.GeneratedAt)})
	}
	return out
}
