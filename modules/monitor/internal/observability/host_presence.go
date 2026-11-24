package observability

import "sort"

// 主机的在线情形。页面的主机卡片和主机类检查（告警的来源）都取 HostPresences 的结论，
// 同一次采集器停报在页面和推送里是同一个判断、同一段文字。
const (
	HostOnline = "online"
	// HostAgentStopped 是采集器停报、但主机网关的组件上报正常：主机在线，只是 host-agent 停了。
	HostAgentStopped = "agent_stopped"
	// HostLost 是采集器停报、主机网关的组件上报也停了：按主机失联处理。
	HostLost = "lost"
)

const (
	hostOnlineReason       = "主机连接正常"
	hostAgentStoppedReason = "主机采集代理（host-agent）超过 3 分钟没有上报，但主机网关仍在上报，主机仍在线；进程退出后 systemd 会在 5 秒后把它拉起，持续未恢复请执行 moox-cli setup logs --host <主机> --component host-agent"
	hostLostReason         = "主机采集代理超过 3 分钟没有上报，主机网关也已停止上报，主机可能宕机或断网"
	hostLostUnsureReason   = "主机采集代理超过 3 分钟没有上报，这台主机没有登记主机网关的上报，主机可能宕机、断网，或采集代理已停止"
)

// HostPresence 是一台登记主机最近一次上报及其在线情形。
type HostPresence struct {
	Host   HostStatus
	State  string
	Reason string
	// Disabled 表示主机或它的 host-agent 部署已停用：不产生失联告警。
	Disabled bool
}

// HostPresences 为每台上报过的登记主机判断在线情形，按 host_id 排序。Monitor 按 host_id 记上报，每台主机一行；
// host_id 不在登记里的上报不参与。拓扑未知时全部参与。
func (o Overview) HostPresences() []HostPresence {
	registered, disabled := map[string]bool{}, map[string]bool{}
	topologyKnown := o.Topology != nil && len(o.Topology.Hosts) > 0
	if o.Topology != nil {
		for _, host := range o.Topology.Hosts {
			registered[host.HostID] = true
			if host.Status == "disabled" {
				disabled[host.HostID] = true
			}
		}
		for _, deployment := range o.Topology.Deployments {
			if deployment.ComponentID == "host-agent" && deployment.Status == "disabled" {
				disabled[deployment.HostID] = true
			}
		}
	}
	var hosts []HostStatus
	for _, item := range o.Hosts {
		if topologyKnown && !registered[item.HostID] {
			continue
		}
		hosts = append(hosts, item)
	}
	// 主机网关的组件上报（经 EventBus）正常才算主机在线，按它区分“采集代理停了”与“主机失联”。
	// EventBus 不可达时各网关的上报一起过期，这时采集代理停报的主机会被判成失联（见监控.md 已知差别）。
	gateway := map[string]string{}
	for _, service := range o.Services {
		if service.ServiceName == "host-gateway" && service.Source == SourceReporter {
			gateway[service.NodeID] = service.ReporterStatus
		}
	}
	out := make([]HostPresence, 0, len(hosts))
	for _, host := range hosts {
		presence := HostPresence{Host: host, State: HostOnline, Reason: hostOnlineReason, Disabled: disabled[host.HostID]}
		if host.Status != "healthy" {
			switch gateway[host.HostID] {
			case "healthy":
				presence.State, presence.Reason = HostAgentStopped, hostAgentStoppedReason
			case "stale", "missing":
				presence.State, presence.Reason = HostLost, hostLostReason
			default:
				presence.State, presence.Reason = HostLost, hostLostUnsureReason
			}
		}
		out = append(out, presence)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host.HostID < out[j].Host.HostID })
	return out
}
