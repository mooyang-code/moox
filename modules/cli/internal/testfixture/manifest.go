package testfixture

import setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"

// SetHost installs a runtime SSH fixture using the canonical manifest model.
func SetHost(manifest *setupconfig.Manifest, host setupconfig.Host, components ...string) {
	if manifest.HostCatalog == nil {
		manifest.HostCatalog = make(map[string]setupconfig.HostDefinition)
	}
	manifest.HostCatalog[host.Name] = setupconfig.HostDefinition{
		Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region,
		Provider: host.Provider, TLSMode: host.TLSMode,
		SSH: setupconfig.SSHConfig{Port: host.Port, Username: host.Username, Password: host.Password},
	}
	if components != nil {
		if manifest.Placements == nil {
			manifest.Placements = make(map[string][]string)
		}
		manifest.Placements[host.Name] = components
	}
}
