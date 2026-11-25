package topology

import (
	"slices"

	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

// Declared 是 moox.toml 的全部主机与部署及启停状态（含管理台终端用的 SSH 登录信息，所以按私有材料处理）：
// 部署控制机时 bootstrap 用它整份覆盖 Admin 的登记，部署其他主机时同一份经 SysDeploy.SyncTopology 发给 Admin。
func (c *Config) Declared() (hostbundle.Topology, error) {
	result := hostbundle.Topology{Version: 1, ControlHostID: c.ControlHost().Name}
	for _, host := range c.Hosts() {
		components := slices.Clone(c.Deployments[host.Name])
		slices.Sort(components)
		disabled := slices.Clone(c.DeploymentsDisabled[host.Name])
		slices.Sort(disabled)
		entry := hostbundle.Host{HostID: host.Name, Label: host.Label, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Components: components,
			Reachability: host.Reachability, ExternalEntry: host.ExternalEntry, Disabled: !host.Enabled, DisabledComponents: disabled}
		ssh, err := host.ConsoleSSH()
		if err != nil {
			return result, err
		}
		if ssh != nil {
			entry.SSH = &hostbundle.SSH{Port: ssh.Port, User: ssh.User, AuthType: "private_key", Secret: ssh.PrivateKey}
		}
		result.Hosts = append(result.Hosts, entry)
	}
	return result, nil
}
