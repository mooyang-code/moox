package topology

import (
	"slices"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// Declared 是 moox.toml 的全部主机与部署及启停状态（含管理台终端用的 SSH 登录信息，所以按私有材料处理）：
// 部署控制机时 bootstrap 用同一份 SyncTopologyReq（protojson）整份覆盖 Admin 的登记，
// 部署其他主机时直接经 SysDeploy.SyncTopology 发给 Admin。
func (c *Config) Declared() (*pb.SyncTopologyReq, error) {
	result := &pb.SyncTopologyReq{Hosts: make([]*pb.SyncTopologyHost, 0, len(c.HostCatalog))}
	for _, host := range c.Hosts() {
		components := slices.Clone(c.Deployments[host.Name])
		slices.Sort(components)
		disabled := slices.Clone(c.DeploymentsDisabled[host.Name])
		slices.Sort(disabled)
		entry := &pb.SyncTopologyHost{
			HostId:               host.Name,
			Enabled:              host.Enabled,
			Label:                host.Label,
			Address:              host.Address,
			PrivateAddress:       host.PrivateAddress,
			Region:               host.Region,
			Reachability:         host.Reachability,
			ExternalEntry:        host.ExternalEntry,
			ComponentIds:         components,
			DisabledComponentIds: disabled,
		}
		ssh, err := host.ConsoleSSH()
		if err != nil {
			return nil, err
		}
		if ssh != nil {
			entry.SshPort = int32(ssh.Port)
			entry.SshUser = ssh.User
			entry.SshAuthType = "private_key"
			entry.SshSecret = ssh.PrivateKey
		}
		result.Hosts = append(result.Hosts, entry)
	}
	return result, nil
}
