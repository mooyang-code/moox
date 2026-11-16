package client

import (
	"context"
	"fmt"
	"slices"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// SyncHostDeployments sends the complete desired business component list for one
// host. Admin injects host components and preserves persisted enablement state.
func (c *Client) SyncHostDeployments(ctx context.Context, cfg *topology.Config, hostID string) error {
	if cfg == nil {
		return fmt.Errorf("setup configuration is required")
	}
	host := cfg.HostByID(hostID)
	if !servicecatalog.ValidHostID(hostID) || !slices.ContainsFunc(cfg.Hosts(), func(h topology.Host) bool { return h.Name == hostID }) {
		return fmt.Errorf("unknown deployment host %q", hostID)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	components := slices.Clone(cfg.Deployments[hostID])
	for _, id := range components {
		component, ok := catalog.Component(id)
		if !ok || component.Scope == servicecatalog.ScopeHost {
			return fmt.Errorf("invalid business component %q", id)
		}
	}
	slices.Sort(components)
	request := &pb.SyncHostDeploymentsReq{
		HostId: hostID, Label: host.Label, Address: host.Address, PrivateAddress: host.PrivateAddress,
		Region: host.Region, Reachability: servicecatalog.ReachabilityName(host.Reachability), ComponentIds: components,
	}
	// 直达主机配了私钥才把 SSH 登录信息交给控制台；其余主机的 SSH 在控制台里置灰（Admin 同时清除旧凭据）。
	ssh, err := host.ConsoleSSH()
	if err != nil {
		return err
	}
	if ssh != nil {
		request.SshPort, request.SshUser, request.SshAuthType, request.SshSecret = int32(ssh.Port), ssh.User, "private_key", ssh.PrivateKey
	}
	response := &pb.SyncHostDeploymentsRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "SyncHostDeployments", request, response); err != nil {
		return err
	}
	if err := checkRetInfo(response.GetRetInfo()); err != nil {
		return err
	}
	return nil
}

// RegistrationState 是 Admin 里一个部署的启用状态：主机停用或该部署停用时，外部程序安装后不启动、不开机自启。
type RegistrationState struct {
	HostEnabled       bool
	DeploymentEnabled bool
}

// DeploymentState 读取主机与组件部署在 Admin 里的启用状态；没有登记返回错误。
func (c *Client) DeploymentState(ctx context.Context, hostID, componentID string) (RegistrationState, error) {
	hosts := &pb.ListDeploymentHostsRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "ListHosts", &pb.ListDeploymentHostsReq{HostId: hostID}, hosts); err != nil {
		return RegistrationState{}, err
	}
	if err := remoteRet("ListHosts", hosts.GetRetInfo()); err != nil {
		return RegistrationState{}, err
	}
	hostAt := slices.IndexFunc(hosts.GetHosts(), func(h *pb.DeploymentHost) bool { return h.GetHostId() == hostID })
	if hostAt < 0 {
		return RegistrationState{}, fmt.Errorf("主机 %s 没有登记在 Admin", hostID)
	}
	deployments := &pb.ListDeploymentsRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "ListDeployments", &pb.ListDeploymentsReq{HostId: hostID, ComponentId: componentID}, deployments); err != nil {
		return RegistrationState{}, err
	}
	if err := remoteRet("ListDeployments", deployments.GetRetInfo()); err != nil {
		return RegistrationState{}, err
	}
	deploymentAt := slices.IndexFunc(deployments.GetDeployments(), func(d *pb.ComponentDeployment) bool {
		return d.GetHostId() == hostID && d.GetComponentId() == componentID
	})
	if deploymentAt < 0 {
		return RegistrationState{}, fmt.Errorf("主机 %s 没有登记部署 %s", hostID, componentID)
	}
	return RegistrationState{
		HostEnabled:       hosts.GetHosts()[hostAt].GetStatus() == servicecatalog.Enabled,
		DeploymentEnabled: deployments.GetDeployments()[deploymentAt].GetStatus() == servicecatalog.Enabled,
	}, nil
}
