package testfixture

import "github.com/mooyang-code/moox/modules/cli/internal/setup/topology"

// SetHost 把一台 SSH 夹具主机写进配置，可选地给它部署组件。
func SetHost(cfg *topology.Config, host topology.Host, components ...string) {
	if cfg.HostCatalog == nil {
		cfg.HostCatalog = make(map[string]topology.HostDefinition)
	}
	cfg.HostCatalog[host.Name] = topology.HostDefinition{
		Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Provider: host.Provider,
		SSH: topology.SSHConfig{Port: host.Port, Username: host.Username, PrivateKeyFile: host.PrivateKeyFile},
	}
	if components != nil {
		if cfg.Deployments == nil {
			cfg.Deployments = make(map[string][]string)
		}
		cfg.Deployments[host.Name] = components
	}
}
