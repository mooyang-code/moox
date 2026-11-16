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
