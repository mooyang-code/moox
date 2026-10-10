package config

import (
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

func (m Manifest) HostByID(id string) Host {
	definition, ok := m.HostCatalog[id]
	if !ok {
		return Host{}
	}
	return Host{Name: id, Address: definition.Address, PrivateAddress: definition.PrivateAddress, Region: definition.Region, Port: definition.SSH.Port, Username: definition.SSH.Username, Password: definition.SSH.Password, Provider: definition.Provider, TLSMode: definition.TLSMode}
}
func (m Manifest) ControlHost() Host     { return m.HostByID(m.PlacementHost("admin")) }
func (m Manifest) CompileHost() Host     { return m.HostByID(m.CompileHostRef.Host) }
func (m Manifest) StorageHost() Host     { return m.HostByID(m.PlacementHost("storage-primary")) }
func (m Manifest) ViewHost() Host        { return m.HostByID(m.PlacementHost("storage-view")) }
func (m Manifest) StrategyHost() Host    { return m.HostByID(m.PlacementHost("strategy")) }
func (m Manifest) HasCompileHost() bool  { return m.CompileHostRef.Host != "" }
func (m Manifest) HasStorageHost() bool  { return m.StorageHost().Name != "" }
func (m Manifest) HasViewHost() bool     { return m.ViewHost().Name != "" }
func (m Manifest) HasStrategyHost() bool { return m.StrategyHost().Name != "" }

func (m Manifest) Hosts() []Host {
	ids := make([]string, 0, len(m.HostCatalog))
	for id := range m.HostCatalog {
		if id == m.CompileHostRef.Host {
			if _, deployed := m.Placements[id]; !deployed {
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
	topology := servicecatalog.Topology{ControlHostID: m.PlacementHost("admin")}
	for _, host := range m.Hosts() {
		topology.Hosts = append(topology.Hosts, servicecatalog.Host{ID: host.Name, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Status: servicecatalog.Enabled})
		for _, component := range catalog.Components {
			if component.Scope == servicecatalog.ScopeHost {
				topology.Placements = append(topology.Placements, servicecatalog.Placement{HostID: host.Name, ComponentID: component.ID, Status: servicecatalog.Enabled})
			}
		}
		for _, id := range m.Placements[host.Name] {
			topology.Placements = append(topology.Placements, servicecatalog.Placement{HostID: host.Name, ComponentID: id, Status: servicecatalog.Enabled})
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
	if m.CompileHostRef.Host != "" {
		if _, ok := m.HostCatalog[m.CompileHostRef.Host]; !ok {
			return fmt.Errorf("config_invalid: compile_host.host must select an existing host ID")
		}
	}
	if err := validatePlacements(m); err != nil {
		return err
	}
	eventbus := m.HostByID(m.PlacementHost("eventbus"))
	if eventbus.Name == "" {
		return fmt.Errorf("config_invalid: placements must include eventbus")
	}
	m.EventBus.PublicAddress = eventbus.Address
	return m.resolveAccessRoutes()
}

// Manifest routes are public candidates. Deployment verifies VPC/subnet
// membership before promoting an endpoint to a private SCF route.
func (m *Manifest) resolveAccessRoutes() error {
	var access []Host
	for _, host := range m.Hosts() {
		if slices.Contains(m.Placements[host.Name], "access") {
			access = append(access, host)
		}
	}
	for i := range m.SCFFetcher.Spaces {
		space := &m.SCFFetcher.Spaces[i]
		space.AccessID, space.AccessHost, space.AccessAddress = "", "", ""
		space.AccessPrivateHost, space.AccessPrivateAddress = "", ""
		space.AccessAddresses, space.AccessIDs = make(map[string]string), make(map[string]string)
		if len(access) == 0 {
			if m.SCFFetcher.Enabled {
				return fmt.Errorf("config_invalid: enabled scf_fetcher requires an access component in placements")
			}
			continue
		}
		primary := access[0]
		if storage := m.StorageHost(); storage.Name != "" {
			for _, host := range access {
				if host.Name == storage.Name {
					primary = host
					break
				}
			}
		}
		space.AccessHost, space.AccessID = primary.Name, "access@"+primary.Name
		space.AccessAddress = net.JoinHostPort(primary.Address, "11004")
		space.AccessPrivateHost = primary.PrivateAddress
		if primary.PrivateAddress != "" {
			space.AccessPrivateAddress = net.JoinHostPort(primary.PrivateAddress, "11004")
		}
		for _, region := range space.Regions {
			target := primary
			for _, host := range access {
				if host.Region != "" && host.Region == strings.ToLower(strings.TrimSpace(region.Region)) {
					target = host
					break
				}
			}
			space.AccessAddresses[region.Region] = net.JoinHostPort(target.Address, "11004")
			space.AccessIDs[region.Region] = "access@" + target.Name
		}
	}
	return nil
}
