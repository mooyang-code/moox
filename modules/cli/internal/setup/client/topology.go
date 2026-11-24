package client

import (
	"context"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

// SyncTopology 把 moox.toml 的整份主机与部署声明（含启停状态）发给 Admin：Admin 按它覆盖登记，多出的主机与部署删除，
// 主机级组件由 Admin 补上。直达主机配了私钥才把 SSH 登录信息交给控制台；其余主机的 SSH 在控制台里置灰（Admin 同时清除旧凭据）。
func (c *Client) SyncTopology(ctx context.Context, cfg *topology.Config) error {
	if cfg == nil {
		return fmt.Errorf("setup configuration is required")
	}
	declared, err := cfg.Declared()
	if err != nil {
		return err
	}
	request := &pb.SyncTopologyReq{}
	for _, host := range declared.Hosts {
		entry := &pb.SyncTopologyHost{
			HostId: host.HostID, Enabled: !host.Disabled, Label: host.Label, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region,
			Reachability: host.Reachability, ExternalEntry: host.ExternalEntry, ComponentIds: host.Components, DisabledComponentIds: host.DisabledComponents,
		}
		if host.SSH != nil {
			entry.SshPort, entry.SshUser, entry.SshAuthType, entry.SshSecret = int32(host.SSH.Port), host.SSH.User, host.SSH.AuthType, host.SSH.Secret
		}
		request.Hosts = append(request.Hosts, entry)
	}
	response := &pb.SyncTopologyRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "SyncTopology", request, response); err != nil {
		return err
	}
	return checkRetInfo(response.GetRetInfo())
}
