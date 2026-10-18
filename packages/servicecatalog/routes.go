package servicecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	// 空值不参与序列化，只有直达主机的拓扑与快照哈希保持不变。
	Reachability string `json:"reachability,omitempty"`
}

// ReachabilityOutbound 标记只能出站的主机：它的网关主动与 control 网关保持反向连接。
const ReachabilityOutbound = "outbound"

// HostGatewayRemotePort 是主机网关的跨主机 TLS 端口。
const HostGatewayRemotePort = 11003

type Placement struct {
	HostID      string `json:"host_id"`
	ComponentID string `json:"component_id"`
	Status      string `json:"status"`
}

type Topology struct {
	ControlHostID string      `json:"control_host_id"`
	Hosts         []Host      `json:"hosts"`
	Placements    []Placement `json:"placements"`
}

type DirectoryHost struct {
	Address        string `json:"address"`
	PrivateAddress string `json:"private_address,omitempty"`
	Region         string `json:"region,omitempty"`
	// Relay 非空时该主机只能出站，调用方要经这台主机（control）的网关打开中转流再连接它。
	Relay string `json:"relay,omitempty"`
}

type Directory struct {
	Version  string                   `json:"version"`
	Services map[string][]string      `json:"services"`
	Hosts    map[string]DirectoryHost `json:"hosts"`
}

// Route grants callers to exactly one service and method. Merging ACL groups
// into separate method/caller sets would accidentally widen permissions.
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

// CompiledHost is a definition, not the final signed-key snapshot. The control
// plane must hash the actual verification key IDs/material along with this
// definition so a key rotation changes the snapshot even without route changes.
type CompiledHost struct {
	HostID              string    `json:"host_id"`
	Disabled            bool      `json:"disabled"`
	Routes              []Route   `json:"routes"`
	Directory           Directory `json:"directory"`
	VerificationCallers []string  `json:"verification_callers"`
	// Relays 只出现在 control 的快照里：每台已启用的出站主机允许打开中转流的端口与调用方。没有出站主机时为空、不参与序列化。
	Relays []RelayTarget `json:"relays,omitempty"`
	Hash   string        `json:"hash"`
}

// RelayTarget 是一台出站主机的中转许可。
type RelayTarget struct {
	HostID string      `json:"host_id"`
	Ports  []RelayPort `json:"ports"`
}

// RelayPort 是允许经中转连接的一个端口与可打开它的调用方：主机网关跨主机端口对能调用该主机任一服务的调用方开放，
// 组件健康端口只对 Monitor 开放。
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
		if !validDirectoryHost(host.ID, DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region}) || !validStatus(host.Status) || host.Reachability != "" && host.Reachability != ReachabilityOutbound {
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
	placements := map[string]Placement{}
	singles := map[string]string{}
	ports := map[string]map[int]string{}
	for _, p := range t.Placements {
		_, hostExists := hosts[p.HostID]
		component, ok := c.Component(p.ComponentID)
		if !hostExists || !ok || !validStatus(p.Status) {
			return fmt.Errorf("invalid placement %s/%s", p.HostID, p.ComponentID)
		}
		key := p.HostID + "/" + p.ComponentID
		if _, exists := placements[key]; exists {
			return fmt.Errorf("duplicate placement %s", key)
		}
		placements[key] = p
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
			if _, ok := placements[t.ControlHostID+"/"+component.ID]; !ok {
				return fmt.Errorf("protected component %q is missing on control", component.ID)
			}
		}
		if component.Scope != ScopeHost {
			continue
		}
		for id := range hosts {
			if _, ok := placements[id+"/"+component.ID]; !ok {
				return fmt.Errorf("host %q is missing automatic placement %q", id, component.ID)
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
	return ports
}

func (c Catalog) Compile(t Topology, hostID string) (CompiledHost, error) {
	if err := c.ValidateTopology(t); err != nil {
		return CompiledHost{}, err
	}
	var current Host
	directory := Directory{Services: map[string][]string{}, Hosts: map[string]DirectoryHost{}}
	hosts := map[string]Host{}
	for _, host := range t.Hosts {
		hosts[host.ID] = host
		if host.ID == hostID {
			current = host
		}
		if host.Status == Enabled {
			entry := DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region}
			if host.Reachability == ReachabilityOutbound {
				entry.Relay = t.ControlHostID
			}
			directory.Hosts[host.ID] = entry
		}
	}
	if current.ID == "" {
		return CompiledHost{}, fmt.Errorf("unknown host %q", hostID)
	}
	compiled := CompiledHost{HostID: hostID, Disabled: current.Status != Enabled, Routes: []Route{}, VerificationCallers: []string{}}
	verification := map[string]bool{}
	for _, placement := range t.Placements {
		if placement.Status != Enabled || hosts[placement.HostID].Status != Enabled {
			continue
		}
		component, _ := c.Component(placement.ComponentID)
		for _, service := range component.Services {
			directory.Services[service.Path] = append(directory.Services[service.Path], placement.HostID)
			if placement.HostID != hostID {
				continue
			}
			for _, method := range service.Methods {
				callers := c.routeCallers(service, method, t.Hosts)
				if len(callers) == 0 {
					continue
				}
				timeout, limit := service.TimeoutFor(method), service.MaxBodyBytes
				if limit == 0 {
					limit = DefaultMaxBodyBytes
				}
				compiled.Routes = append(compiled.Routes, Route{ComponentID: component.ID, ServicePath: service.Path, Method: method, Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(service.Port)), TimeoutMS: timeout, MaxBodyBytes: limit, ReadOnly: slices.Contains(service.ReadOnlyMethods, method), Callers: callers})
				for _, caller := range callers {
					verification[caller] = true
				}
			}
		}
	}
	for _, ids := range directory.Services {
		sort.Strings(ids)
	}
	version, err := contentHash(directory)
	if err != nil {
		return CompiledHost{}, err
	}
	directory.Version = version
	compiled.Directory = directory
	sort.Slice(compiled.Routes, func(i, j int) bool {
		left, right := compiled.Routes[i], compiled.Routes[j]
		if left.ServicePath != right.ServicePath {
			return left.ServicePath < right.ServicePath
		}
		return left.Method < right.Method
	})
	if hostID == t.ControlHostID {
		compiled.Relays = c.relayTargets(t, hosts)
		// control 要校验打开中转流的签名，所以这些调用方也要在 control 的校验名单里。
		for _, relay := range compiled.Relays {
			for _, port := range relay.Ports {
				for _, caller := range port.Callers {
					verification[caller] = true
				}
			}
		}
	}
	for caller := range verification {
		compiled.VerificationCallers = append(compiled.VerificationCallers, caller)
	}
	sort.Strings(compiled.VerificationCallers)
	compiled.Hash, err = contentHash(compiled)
	return compiled, err
}

// relayTargets 计算各台已启用出站主机的中转许可：跨主机端口的调用方是该主机上全部路由调用方的并集，
// 组件健康端口只允许 Monitor。
func (c Catalog) relayTargets(t Topology, hosts map[string]Host) []RelayTarget {
	var relays []RelayTarget
	for _, host := range t.Hosts {
		if host.Reachability != ReachabilityOutbound || host.Status != Enabled {
			continue
		}
		gateway := map[string]bool{}
		health := map[int]bool{}
		for _, placement := range t.Placements {
			if placement.HostID != host.ID || placement.Status != Enabled {
				continue
			}
			component, _ := c.Component(placement.ComponentID)
			if component.Health.Port > 0 {
				health[component.Health.Port] = true
			}
			for _, service := range component.Services {
				for _, method := range service.Methods {
					for _, caller := range c.routeCallers(service, method, t.Hosts) {
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
		ports := make([]int, 0, len(health))
		for port := range health {
			ports = append(ports, port)
		}
		sort.Ints(ports)
		for _, port := range ports {
			target.Ports = append(target.Ports, RelayPort{Port: port, Callers: []string{"monitor"}})
		}
		relays = append(relays, target)
	}
	return relays
}

// ValidRelay 校验一条中转许可：端口不重复，跨主机端口的调用方是合法 ID 且不重复，
// 其余端口必须是服务目录里某个组件的健康端口并且只对 Monitor 开放。
func (c Catalog) ValidRelay(r RelayTarget) bool {
	if !ValidHostID(r.HostID) || len(r.Ports) > 64 {
		return false
	}
	seen := map[int]bool{}
	for _, port := range r.Ports {
		if seen[port.Port] || len(port.Callers) == 0 || len(port.Callers) > 2048 {
			return false
		}
		seen[port.Port] = true
		if port.Port != HostGatewayRemotePort {
			if !slices.Equal(port.Callers, []string{"monitor"}) || !c.healthPort(port.Port) {
				return false
			}
			continue
		}
		callers := map[string]bool{}
		for _, caller := range port.Callers {
			if caller == "" || len(caller) > 128 || callers[caller] {
				return false
			}
			callers[caller] = true
		}
	}
	return true
}

func (c Catalog) healthPort(port int) bool {
	for _, component := range c.Components {
		if component.Health.Port == port && port > 0 {
			return true
		}
	}
	return false
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

func (c Catalog) routeCallers(service Service, method string, hosts []Host) []string {
	set := map[string]bool{}
	for _, grant := range service.ACL {
		if !slices.Contains(grant.Methods, method) {
			continue
		}
		for _, caller := range grant.Callers {
			if caller == "host-gateway@*" {
				// Disabled hosts must still authenticate to obtain their disabled
				// snapshot. Removed hosts disappear from this scope immediately.
				for _, host := range hosts {
					set["host-gateway@"+host.ID] = true
				}
			} else {
				set[caller] = true
			}
		}
	}
	if c.externallyAllowed(service.Path, method) {
		set["access"] = true
	}
	callers := make([]string, 0, len(set))
	for caller := range set {
		callers = append(callers, caller)
	}
	sort.Strings(callers)
	return callers
}

func contentHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("hash catalog definition: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
