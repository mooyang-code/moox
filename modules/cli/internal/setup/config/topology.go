package config

import (
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

func (m Manifest) HostByID(id string) Host {
	definition, ok := m.HostCatalog[id]
	if !ok {
		return Host{}
	}
	reachability, _ := servicecatalog.ParseReachability(definition.Reachability)
	return Host{Name: id, Address: definition.Address, PrivateAddress: definition.PrivateAddress, Region: definition.Region, Port: definition.SSH.Port, Username: definition.SSH.Username, Password: definition.SSH.Password, Provider: definition.Provider, TLSMode: definition.TLSMode, Reachability: reachability, Label: definition.Label}
}
func (m Manifest) ControlHost() Host  { return m.HostByID(m.ComponentHost("admin")) }
func (m Manifest) StorageHost() Host  { return m.HostByID(m.ComponentHost("storage-primary")) }
func (m Manifest) ViewHost() Host     { return m.HostByID(m.ComponentHost("storage-view")) }
func (m Manifest) StrategyHost() Host { return m.HostByID(m.ComponentHost("strategy")) }

// FactorEngineHost 返回因子引擎所在的主机；未配置或在本机（local）时返回空 Host。
func (m Manifest) FactorEngineHost() Host {
	if m.FactorEngineRef.Host == FactorEngineLocal {
		return Host{}
	}
	return m.HostByID(m.FactorEngineRef.Host)
}

// FactorEngineIsLocal 表示因子引擎运行在执行 moox-cli 的这台电脑上。
func (m Manifest) FactorEngineIsLocal() bool { return m.FactorEngineRef.Host == FactorEngineLocal }
func (m Manifest) HasStorageHost() bool      { return m.StorageHost().Name != "" }
func (m Manifest) HasViewHost() bool         { return m.ViewHost().Name != "" }
func (m Manifest) HasStrategyHost() bool     { return m.StrategyHost().Name != "" }

func (m Manifest) Hosts() []Host {
	ids := make([]string, 0, len(m.HostCatalog))
	for id := range m.HostCatalog {
		// 因子计算机只登记登录方式；没有放置组件时不是部署主机。
		if id == m.FactorEngineRef.Host {
			if _, deployed := m.Deployments[id]; !deployed {
				continue
			}
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	control := m.ControlHost().Name
	if index := slices.Index(ids, control); index > 0 {
		copy(ids[1:index+1], ids[:index])
		ids[0] = control
	}
	hosts := make([]Host, 0, len(ids))
	for _, id := range ids {
		hosts = append(hosts, m.HostByID(id))
	}
	return hosts
}
func (m Manifest) OtherHosts() []Host {
	control := m.ControlHost().Name
	return slices.DeleteFunc(m.Hosts(), func(host Host) bool { return host.Name == control })
}

// Topology is the shared deployment model, including code-owned host components.
func (m Manifest) Topology() (servicecatalog.Topology, error) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return servicecatalog.Topology{}, err
	}
	topology := servicecatalog.Topology{ControlHostID: m.ComponentHost("admin")}
	for _, host := range m.Hosts() {
		topology.Hosts = append(topology.Hosts, servicecatalog.Host{ID: host.Name, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Status: servicecatalog.Enabled, Reachability: host.Reachability})
		for _, component := range catalog.Components {
			if component.Scope == servicecatalog.ScopeHost {
				topology.Deployments = append(topology.Deployments, servicecatalog.Deployment{HostID: host.Name, ComponentID: component.ID, Status: servicecatalog.Enabled})
			}
		}
		for _, id := range m.Deployments[host.Name] {
			topology.Deployments = append(topology.Deployments, servicecatalog.Deployment{HostID: host.Name, ComponentID: id, Status: servicecatalog.Enabled})
		}
	}
	return topology, nil
}

func validateHostCatalog(catalog map[string]HostDefinition) error {
	if len(catalog) == 0 || len(catalog) > 1025 {
		return fmt.Errorf("config_invalid: hosts requires 1..1025 definitions")
	}
	seen := make(map[string]string)
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		definition := catalog[id]
		if !servicecatalog.ValidHostID(id) {
			return fmt.Errorf("config_invalid: hosts keys must be canonical host IDs, not addresses; use [hosts.<host-id>] with address and ssh")
		}
		path := "hosts." + id
		if !servicecatalog.ValidHostAddress(definition.Address) {
			return fmt.Errorf("config_invalid: %s.address must be an IP address or DNS hostname", path)
		}
		if definition.PrivateAddress != "" && !servicecatalog.ValidHostAddress(definition.PrivateAddress) {
			return fmt.Errorf("config_invalid: %s.private_address must be an IP address or DNS hostname", path)
		}
		if len(definition.Region) > 128 {
			return fmt.Errorf("config_invalid: %s.region exceeds 128 bytes", path)
		}
		if _, ok := servicecatalog.ParseReachability(definition.Reachability); !ok {
			return fmt.Errorf("config_invalid: %s.reachability must be direct or outbound", path)
		}
		address := strings.ToLower(strings.TrimSuffix(definition.Address, "."))
		if ip := net.ParseIP(address); ip != nil {
			address = ip.String()
		}
		if previous, exists := seen[address]; exists {
			return fmt.Errorf("config_invalid: %s.address duplicates hosts.%s.address", path, previous)
		}
		seen[address] = id
		if definition.SSH.Port < 1 || definition.SSH.Port > 65535 {
			return fmt.Errorf("config_invalid: %s.ssh.port must be between 1 and 65535", path)
		}
		if definition.SSH.Username == "" {
			return fmt.Errorf("config_invalid: %s.ssh.username is required", path)
		}
		if utf8.RuneCountInString(definition.Label) > 32 || strings.ContainsAny(definition.Label, "\r\n\t") {
			return fmt.Errorf("config_invalid: %s.label must be a single line of at most 32 characters", path)
		}
		if definition.TLSMode != "" && definition.TLSMode != "auto" && definition.TLSMode != "public" && definition.TLSMode != "internal" {
			return fmt.Errorf("config_invalid: %s.tls_mode must be auto, public, or internal", path)
		}
		if definition.Provider != "" && !providerPattern.MatchString(definition.Provider) {
			return fmt.Errorf("config_invalid: %s.provider must use lowercase letters, digits, dash, or underscore", path)
		}
	}
	return nil
}

func resolveManifestReferences(m *Manifest) error {
	if err := validateHostCatalog(m.HostCatalog); err != nil {
		return err
	}
	if ref := m.FactorEngineRef.Host; ref != "" && ref != FactorEngineLocal {
		if _, ok := m.HostCatalog[ref]; !ok {
			return fmt.Errorf("config_invalid: factor_engine.host must be \"local\" or an existing host ID")
		}
	}
	if err := validateDeployments(m); err != nil {
		return err
	}
	eventbus := m.HostByID(m.ComponentHost("eventbus"))
	if eventbus.Name == "" {
		return fmt.Errorf("config_invalid: deployments must include eventbus")
	}
	m.EventBus.PublicAddress = eventbus.Address
	return nil
}
