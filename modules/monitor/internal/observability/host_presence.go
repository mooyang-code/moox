package observability

import "sort"

// 主机的在线情形。页面的主机卡片和主机类检查（告警的来源）都取 HostPresences 的结论，
// 同一次采集器停报在页面和推送里是同一个判断、同一段文字。
const (
	HostOnline = "online"
	// HostAgentStopped 是采集器停报、但主机网关心跳正常：主机在线，只是 host-agent 停了。
	HostAgentStopped = "agent_stopped"
	// HostLost 是采集器停报、主机网关心跳也不正常：按主机失联处理。
	HostLost = "lost"
)

const (
	hostOnlineReason       = "主机连接正常"
	hostAgentStoppedReason = "主机采集代理（host-agent）超过 3 分钟没有上报，但主机网关心跳正常，主机仍在线；主机守护应在 1 分钟内把它拉起，持续未恢复请查看该主机的 run/logs/host-agent.log"
	hostLostReason         = "主机采集代理超过 3 分钟没有上报，主机网关心跳也已停止，主机可能宕机或断网"
	hostLostUnsureReason   = "主机采集代理超过 3 分钟没有上报，主机网关心跳无法确认，主机可能宕机、断网，或采集代理已停止"
)

// HostPresence 是一台登记主机当前参与汇总的采集器及其在线情形。
type HostPresence struct {
	Host   HostStatus
	State  string
	Reason string
	// Disabled 表示主机或它的 host-agent 部署已停用：不产生失联告警。
	Disabled bool
}

// HostPresences 为每台登记主机选出参与汇总的采集器并判断在线情形，按 host_id 排序。
// 同一主机有多个采集器（例如重装后换了 agent_id）时只取最近上报的；没有 host_id、或 host_id 不在登记里的采集器不参与。
// 拓扑未知时只按 host_id 取最近上报的。
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
	latest := map[string]HostStatus{}
	for _, item := range o.Hosts {
		if item.HostID == "" || topologyKnown && !registered[item.HostID] {
			continue
		}
		if current, ok := latest[item.HostID]; !ok || item.LastSeenAt.After(current.LastSeenAt) || item.LastSeenAt.Equal(current.LastSeenAt) && item.AgentID > current.AgentID {
			latest[item.HostID] = item
		}
	}
	// 网关心跳确认正常才算主机在线。某一次读取网关状态出错不改变结论：库里保留上一次读到的心跳时间，
	// 它超过 2 分钟自然判为停止，这时才改判失联；否则读取时好时坏会让失联与停报来回切换。
	heartbeat := map[string]string{}
	for _, signal := range o.GatewaySignals {
		if signal.Kind == "heartbeat" {
			heartbeat[signal.HostID] = signal.Status
		}
	}
	out := make([]HostPresence, 0, len(latest))
	for _, host := range latest {
		presence := HostPresence{Host: host, State: HostOnline, Reason: hostOnlineReason, Disabled: disabled[host.HostID]}
		if host.Status != "healthy" {
			switch heartbeat[host.HostID] {
			case "healthy":
				presence.State, presence.Reason = HostAgentStopped, hostAgentStoppedReason
			case "down":
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
