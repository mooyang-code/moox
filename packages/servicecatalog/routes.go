package servicecatalog

import (
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type Host struct {
	ID             string `json:"id"`
	Address        string `json:"address"`
	PrivateAddress string `json:"private_address,omitempty"`
	Region         string `json:"region,omitempty"`
	Status         string `json:"status"`
	// Reachability 为空表示可直达；ReachabilityOutbound 表示只能出站（家庭网络等），进入它的连接经 control 网关中转。
	Reachability string `json:"reachability,omitempty"`
}

// ReachabilityOutbound 标记只能出站的主机：它的网关主动与 control 网关保持反向连接。
const ReachabilityOutbound = "outbound"

// ReachabilityDirect 是配置与 Admin 登记里“可直达”的写法，拓扑里对应空值。
const ReachabilityDirect = "direct"

// ParseReachability 把配置或登记里的可达性（空、direct、outbound）转换成拓扑取值（空或 outbound）。
func ParseReachability(value string) (string, bool) {
	switch value {
	case "", ReachabilityDirect:
		return "", true
	case ReachabilityOutbound:
		return ReachabilityOutbound, true
	}
	return "", false
}

// ReachabilityName 返回拓扑取值在配置与登记里的写法。
func ReachabilityName(value string) string {
	if value == ReachabilityOutbound {
		return ReachabilityOutbound
	}
	return ReachabilityDirect
}

// HostGatewayRemotePort 是主机网关的跨主机 TLS 端口。
const HostGatewayRemotePort = 11003

// adminComponent 只部署在控制机上：按它认控制机，部署了它的主机另有 console 这个调用方。
const adminComponent = "admin"

type Deployment struct {
	HostID      string `json:"host_id"`
	ComponentID string `json:"component_id"`
	Status      string `json:"status"`
}

type Topology struct {
	ControlHostID string       `json:"control_host_id"`
	Hosts         []Host       `json:"hosts"`
	Deployments   []Deployment `json:"deployments"`
}

// NewTopology 用快照或目录下发的主机与部署拼出拓扑。控制机不随快照下发：它就是部署了 admin 的那台。
func NewTopology(hosts []Host, deployments []Deployment) Topology {
	t := Topology{Hosts: hosts, Deployments: deployments}
	for _, deployment := range deployments {
		if deployment.ComponentID == adminComponent {
			t.ControlHostID = deployment.HostID
		}
	}
	return t
}

// DirectoryHost 是目录里一台启用的主机。
type DirectoryHost struct {
	Address        string `json:"address"`
	PrivateAddress string `json:"private_address,omitempty"`
	// Relay 非空时该主机只能出站，调用方要经这台主机（control）的网关打开中转流再连接它。
	Relay string `json:"relay,omitempty"`
}

// Directory 是主机与部署按内嵌目录展开后的结果：每个服务在哪些主机上、主机怎么连。Version 由下发方给出，只用来判断有没有变化。
type Directory struct {
	Version  string                   `json:"version"`
	Services map[string][]string      `json:"services"`
	Hosts    map[string]DirectoryHost `json:"hosts"`
}

// Route 是一个方法和能调它的调用方，各主机的网关按拓扑与内嵌目录自己算，不随快照下发。
type Route struct {
	ComponentID  string   `json:"component_id"`
	ServicePath  string   `json:"service_path"`
	Method       string   `json:"method"`
	Address      string   `json:"address"`
	TimeoutMS    int64    `json:"timeout_ms"`
	MaxBodyBytes int64    `json:"max_body_bytes"`
	ReadOnly     bool     `json:"read_only"`
	Callers      []string `json:"callers"`
}

// CompiledHost 是一台主机按拓扑与内嵌目录算出的本机视图。
type CompiledHost struct {
	HostID    string
	Disabled  bool
	Routes    []Route
	Directory Directory
	// Relays 只有 control 才有：每台已启用的出站主机允许打开中转流的端口与调用方。
	Relays []RelayTarget
	// Unknown 是拓扑里本进程的内嵌目录不认识的组件（两边版本不一致时出现），它们被忽略，其余照常生效。
	Unknown []string
}

// RelayTarget 是一台出站主机的中转许可。
type RelayTarget struct {
	HostID string      `json:"host_id"`
	Ports  []RelayPort `json:"ports"`
}

// RelayPort 是允许经中转连接的一个端口与可打开它的调用方：主机网关跨主机端口对能调用该主机任一服务的调用方开放，
// 组件健康端口只对 Monitor 与 moox-cli 开放，外部程序的 relay_http 端口只对它声明的调用方开放。
type RelayPort struct {
	Port    int      `json:"port"`
	Callers []string `json:"callers"`
}

func (c Catalog) ValidateTopology(t Topology) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if len(t.Hosts) == 0 || len(t.Hosts) > 1024 {
		return fmt.Errorf("topology requires 1..1024 hosts")
	}
	hosts := map[string]Host{}
	addresses := map[string]bool{}
	for _, host := range t.Hosts {
		key := strings.ToLower(host.Address)
		if ip := net.ParseIP(host.Address); ip != nil {
			key = ip.String()
		}
		if !validDirectoryHost(host.ID, DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress}) || len(host.Region) > 128 || !validStatus(host.Status) || host.Reachability != "" && host.Reachability != ReachabilityOutbound {
			return fmt.Errorf("invalid host %q", host.ID)
		}
		if _, exists := hosts[host.ID]; exists || addresses[key] {
			return fmt.Errorf("duplicate host ID or address %q", host.ID)
		}
		hosts[host.ID], addresses[key] = host, true
	}
	control, exists := hosts[t.ControlHostID]
	if !exists || control.Status != Enabled {
		return fmt.Errorf("control host must exist and be enabled")
	}
	if control.Reachability == ReachabilityOutbound {
		return fmt.Errorf("control host must be directly reachable")
	}
	deployments := map[string]Deployment{}
	singles := map[string]string{}
	ports := map[string]map[int]string{}
	for _, p := range t.Deployments {
		_, hostExists := hosts[p.HostID]
		component, ok := c.Component(p.ComponentID)
		if !hostExists || !ok || !validStatus(p.Status) {
			return fmt.Errorf("invalid deployment %s/%s", p.HostID, p.ComponentID)
		}
		key := p.HostID + "/" + p.ComponentID
		if _, exists := deployments[key]; exists {
			return fmt.Errorf("duplicate deployment %s", key)
		}
		deployments[key] = p
		if component.Scope == ScopeControl && p.HostID != t.ControlHostID {
			return fmt.Errorf("control component %q cannot be placed on %q", component.ID, p.HostID)
		}
		if (component.Protected || component.Scope == ScopeHost) && p.Status != Enabled {
			return fmt.Errorf("protected or host component %q cannot be disabled", component.ID)
		}
		if p.Status != Enabled {
			continue
		}
		if component.Scope != ScopeHost && component.Replicas == Single {
			if previous := singles[component.ID]; previous != "" {
				return fmt.Errorf("single component %q is enabled on both %q and %q", component.ID, previous, p.HostID)
			}
			singles[component.ID] = p.HostID
		}
		if ports[p.HostID] == nil {
			ports[p.HostID] = map[int]string{}
		}
		for _, port := range componentPorts(component) {
			if previous := ports[p.HostID][port]; previous != "" {
				return fmt.Errorf("port conflict on host %q: %s and %s use %d", p.HostID, previous, component.ID, port)
			}
			ports[p.HostID][port] = component.ID
		}
	}
	for _, component := range c.Components {
		if component.Protected && component.Scope == ScopeControl {
			if _, ok := deployments[t.ControlHostID+"/"+component.ID]; !ok {
				return fmt.Errorf("protected component %q is missing on control", component.ID)
			}
		}
		if component.Scope != ScopeHost {
			continue
		}
		for id := range hosts {
			if _, ok := deployments[id+"/"+component.ID]; !ok {
				return fmt.Errorf("host %q is missing automatic deployment %q", id, component.ID)
			}
		}
	}
	return nil
}

func validStatus(status string) bool { return status == Enabled || status == Disabled }

func componentPorts(component Component) []int {
	ports := slices.Clone(component.Ports)
	if component.Health.Port != 0 {
		ports = append(ports, component.Health.Port)
	}
	for _, service := range component.Services {
		ports = append(ports, service.Port)
	}
	if component.RelayHTTP != nil {
		ports = append(ports, component.RelayHTTP.Port)
	}
	return ports
}

// Resolve 把主机与部署展开成目录：只算启用主机上启用的部署，出站主机由控制机中转。不认识的组件跳过，随 unknown 返回（排序、去重）。
func (c Catalog) Resolve(t Topology) (directory Directory, unknown []string) {
	directory = Directory{Services: map[string][]string{}, Hosts: map[string]DirectoryHost{}}
	for _, host := range t.Hosts {
		if host.Status != Enabled {
			continue
		}
		entry := DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress}
		if host.Reachability == ReachabilityOutbound {
			entry.Relay = t.ControlHostID
		}
		directory.Hosts[host.ID] = entry
	}
	for _, deployment := range t.Deployments {
		component, ok := c.Component(deployment.ComponentID)
		if !ok {
			unknown = append(unknown, deployment.ComponentID)
			continue
		}
		if _, enabled := directory.Hosts[deployment.HostID]; !enabled || deployment.Status != Enabled {
			continue
		}
		for _, service := range component.Services {
			directory.Services[service.Path] = append(directory.Services[service.Path], deployment.HostID)
		}
	}
	for _, ids := range directory.Services {
		sort.Strings(ids)
	}
	sort.Strings(unknown)
	return directory, slices.Compact(unknown)
}

// Compile 算出一台主机的本机视图。拓扑的合法性由 Admin 在写入时校验（ValidateTopology），这里不再校验；
// 不认识的组件忽略，其余路由照常生效。
func (c Catalog) Compile(t Topology, hostID string) (CompiledHost, error) {
	current := slices.IndexFunc(t.Hosts, func(host Host) bool { return host.ID == hostID })
	if current < 0 {
		return CompiledHost{}, fmt.Errorf("unknown host %q", hostID)
	}
	compiled := CompiledHost{HostID: hostID, Disabled: t.Hosts[current].Status != Enabled, Routes: []Route{}}
	compiled.Directory, compiled.Unknown = c.Resolve(t)
	if compiled.Disabled {
		return compiled, nil
	}
	for _, deployment := range t.Deployments {
		component, ok := c.Component(deployment.ComponentID)
		if !ok || deployment.HostID != hostID || deployment.Status != Enabled {
			continue
		}
		for _, service := range component.Services {
			limit := service.MaxBodyBytes
			if limit == 0 {
				limit = DefaultMaxBodyBytes
			}
			for _, method := range service.Methods {
				if callers := c.routeCallers(method); len(callers) > 0 {
					compiled.Routes = append(compiled.Routes, Route{ComponentID: component.ID, ServicePath: service.Path, Method: method.Name, Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(service.Port)), TimeoutMS: service.TimeoutFor(method.Name), MaxBodyBytes: limit, ReadOnly: method.ReadOnly, Callers: callers})
				}
			}
		}
	}
	sort.Slice(compiled.Routes, func(i, j int) bool {
		left, right := compiled.Routes[i], compiled.Routes[j]
		if left.ServicePath != right.ServicePath {
			return left.ServicePath < right.ServicePath
		}
		return left.Method < right.Method
	})
	if hostID == t.ControlHostID {
		compiled.Relays = c.relayTargets(t)
	}
	return compiled, nil
}

// relayTargets 计算各台已启用出站主机的中转许可：跨主机端口的调用方是该主机上全部路由调用方的并集，
// relay_http 端口只允许声明的调用方。组件健康端口不经中转（组件健康只看上报，设计 3.8）。
// 跨主机端口在前，其余端口按端口号升序。
func (c Catalog) relayTargets(t Topology) []RelayTarget {
	var relays []RelayTarget
	for _, host := range t.Hosts {
		if host.Reachability != ReachabilityOutbound || host.Status != Enabled {
			continue
		}
		gateway := map[string]bool{}
		local := map[int][]string{}
		for _, deployment := range t.Deployments {
			if deployment.HostID != host.ID || deployment.Status != Enabled {
				continue
			}
			component, _ := c.Component(deployment.ComponentID) // 不认识的组件是零值：没有端口，也没有服务
			if component.RelayHTTP != nil {
				local[component.RelayHTTP.Port] = relayHTTPCallers(*component.RelayHTTP)
			}
			for _, service := range component.Services {
				for _, method := range service.Methods {
					for _, caller := range c.routeCallers(method) {
						gateway[caller] = true
					}
				}
			}
		}
		target := RelayTarget{HostID: host.ID, Ports: []RelayPort{}}
		if len(gateway) > 0 {
			callers := make([]string, 0, len(gateway))
			for caller := range gateway {
				callers = append(callers, caller)
			}
			sort.Strings(callers)
			target.Ports = append(target.Ports, RelayPort{Port: HostGatewayRemotePort, Callers: callers})
		}
		ports := make([]int, 0, len(local))
		for port := range local {
			ports = append(ports, port)
		}
		sort.Ints(ports)
		for _, port := range ports {
			target.Ports = append(target.Ports, RelayPort{Port: port, Callers: slices.Clone(local[port])})
		}
		relays = append(relays, target)
	}
	return relays
}

// relayHTTPCallers 返回 relay_http 端口的中转调用方（排序后的副本）。
func relayHTTPCallers(relay RelayHTTP) []string {
	callers := slices.Clone(relay.Callers)
	sort.Strings(callers)
	return callers
}

// RelayHTTPCallers 返回端口作为某个外部程序 relay_http 端口时的中转调用方（排序后）；不是时 ok 为假。
func (c Catalog) RelayHTTPCallers(port int) (callers []string, ok bool) {
	for _, component := range c.Components {
		if component.RelayHTTP != nil && component.RelayHTTP.Port == port && port > 0 {
			return relayHTTPCallers(*component.RelayHTTP), true
		}
	}
	return nil, false
}

// LocalRelayPort 报告出站主机的网关能否把中转流接到本机这个端口：只有外部程序的 relay_http 端口。
func (c Catalog) LocalRelayPort(port int) bool {
	_, relayHTTP := c.RelayHTTPCallers(port)
	return relayHTTP
}

// Allowed 报告调用方能否经中转连接出站主机的指定端口。
func (r RelayTarget) Allowed(caller string, port int) bool {
	for _, p := range r.Ports {
		if p.Port == port {
			return slices.Contains(p.Callers, caller)
		}
	}
	return false
}

// routeCallers 是网关上这条路由的调用方：人操作身份按 operators，外部身份统一由 access 代为转发，其余照 callers。
func (c Catalog) routeCallers(method Method) []string {
	set := map[string]bool{}
	for _, operator := range method.OperatorList() {
		set[operator] = true
	}
	for _, caller := range method.Callers {
		if c.ExternalIdentity(caller) {
			set["access"] = true
		} else {
			set[caller] = true
		}
	}
	callers := make([]string, 0, len(set))
	for caller := range set {
		callers = append(callers, caller)
	}
	sort.Strings(callers)
	return callers
}

// SigningCallers 返回每台已登记主机上可以用主机密钥签名的调用方（排序后）：这台主机上启用的组件（第三方程序不签名），
// 部署了 admin 的主机另有 console（管理台代浏览器调用时的身份）。每台主机的网关都在内：停用的主机没有别的调用方，
// 但它的网关仍要验签取到“已停用”的快照。
func (c Catalog) SigningCallers(t Topology) map[string][]string {
	enabled := map[string]bool{}
	callers := map[string][]string{}
	for _, host := range t.Hosts {
		enabled[host.ID] = host.Status == Enabled
		callers[host.ID] = []string{hostGatewayCaller}
	}
	for _, deployment := range t.Deployments {
		component, ok := c.Component(deployment.ComponentID)
		if !ok || component.ThirdParty || deployment.Status != Enabled || !enabled[deployment.HostID] {
			continue
		}
		callers[deployment.HostID] = append(callers[deployment.HostID], component.ID)
		if component.ID == adminComponent {
			callers[deployment.HostID] = append(callers[deployment.HostID], OperatorConsole)
		}
	}
	for id := range callers {
		sort.Strings(callers[id])
		callers[id] = slices.Compact(callers[id])
	}
	return callers
}
