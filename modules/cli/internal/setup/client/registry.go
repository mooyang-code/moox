package client

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// HostRegistration 是 Admin 里一台主机的登记状态。
type HostRegistration struct {
	HostID string `json:"host_id"`
	Status string `json:"status"`
}

// DeploymentRegistration 是 Admin 里一个组件部署的登记状态。
type DeploymentRegistration struct {
	HostID      string `json:"host_id"`
	ComponentID string `json:"component_id"`
	Status      string `json:"status"`
}

const registryPageSize = 100

// Registry 读出 Admin 里全部主机与组件部署的登记状态。
func (c *Client) Registry(ctx context.Context) ([]HostRegistration, []DeploymentRegistration, error) {
	var hosts []HostRegistration
	for page := uint32(1); ; page++ {
		rsp := &pb.ListDeploymentHostsRsp{}
		if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "ListHosts", &pb.ListDeploymentHostsReq{Page: &pb.Page{Page: page, Size: registryPageSize}}, rsp); err != nil {
			return nil, nil, err
		}
		if err := remoteRet("ListHosts", rsp.GetRetInfo()); err != nil {
			return nil, nil, err
		}
		for _, host := range rsp.GetHosts() {
			hosts = append(hosts, HostRegistration{HostID: host.GetHostId(), Status: host.GetStatus()})
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	var deployments []DeploymentRegistration
	for page := uint32(1); ; page++ {
		rsp := &pb.ListDeploymentsRsp{}
		if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "ListDeployments", &pb.ListDeploymentsReq{Page: &pb.Page{Page: page, Size: registryPageSize}}, rsp); err != nil {
			return nil, nil, err
		}
		if err := remoteRet("ListDeployments", rsp.GetRetInfo()); err != nil {
			return nil, nil, err
		}
		for _, deployment := range rsp.GetDeployments() {
			deployments = append(deployments, DeploymentRegistration{HostID: deployment.GetHostId(), ComponentID: deployment.GetComponentId(), Status: deployment.GetStatus()})
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return hosts, deployments, nil
}

// SetHostStatus 修改主机的登记状态（enabled 或 disabled）。
func (c *Client) SetHostStatus(ctx context.Context, hostID, status string) error {
	rsp := &pb.SetHostStatusRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "SetHostStatus", &pb.SetHostStatusReq{HostId: hostID, Status: status}, rsp); err != nil {
		return err
	}
	return remoteRet("SetHostStatus", rsp.GetRetInfo())
}

// SetDeploymentStatus 修改组件部署的登记状态（enabled 或 disabled）。
func (c *Client) SetDeploymentStatus(ctx context.Context, hostID, componentID, status string) error {
	rsp := &pb.SetDeploymentStatusRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "SetDeploymentStatus", &pb.SetDeploymentStatusReq{HostId: hostID, ComponentId: componentID, Status: status}, rsp); err != nil {
		return err
	}
	return remoteRet("SetDeploymentStatus", rsp.GetRetInfo())
}
