package client

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// RegisteredHost 是 Admin 拓扑里一台主机的概要（不含任何秘密）。
type RegisteredHost struct {
	HostID     string   `json:"host_id"`
	Address    string   `json:"address"`
	Components []string `json:"components"`
}

// RegisteredHostByID 读出 Admin 拓扑里的主机与它的组件部署；没有登记时返回 found=false。
func (c *Client) RegisteredHostByID(ctx context.Context, hostID string) (RegisteredHost, bool, error) {
	if !servicecatalog.ValidHostID(hostID) {
		return RegisteredHost{}, false, fmt.Errorf("invalid host ID %q", hostID)
	}
	hosts := &pb.ListDeploymentHostsRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "ListHosts", &pb.ListDeploymentHostsReq{HostId: hostID}, hosts); err != nil {
		return RegisteredHost{}, false, err
	}
	if err := remoteRet("ListHosts", hosts.GetRetInfo()); err != nil {
		return RegisteredHost{}, false, err
	}
	i := slices.IndexFunc(hosts.GetHosts(), func(h *pb.DeploymentHost) bool { return h.GetHostId() == hostID })
	if i < 0 {
		return RegisteredHost{}, false, nil
	}
	host := RegisteredHost{HostID: hostID, Address: hosts.GetHosts()[i].GetAddress(), Components: []string{}}
	deployments := &pb.ListDeploymentsRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "ListDeployments", &pb.ListDeploymentsReq{HostId: hostID}, deployments); err != nil {
		return RegisteredHost{}, false, err
	}
	if err := remoteRet("ListDeployments", deployments.GetRetInfo()); err != nil {
		return RegisteredHost{}, false, err
	}
	for _, p := range deployments.GetDeployments() {
		if p.GetHostId() == hostID {
			host.Components = append(host.Components, p.GetComponentId())
		}
	}
	slices.Sort(host.Components)
	return host, true, nil
}

// RemoveRegisteredHost 从 Admin 拓扑删除主机：先清空业务组件部署（网关路由随之撤销），再删除主机；
// 主机的 SSH 登录信息随主机一起删除。主机删除后它的网关签名身份不再被任何网关接受。
func (c *Client) RemoveRegisteredHost(ctx context.Context, host RegisteredHost) error {
	sync := &pb.SyncHostDeploymentsRsp{}
	listed := &pb.ListDeploymentHostsRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "ListHosts", &pb.ListDeploymentHostsReq{HostId: host.HostID}, listed); err != nil {
		return err
	}
	if err := remoteRet("ListHosts", listed.GetRetInfo()); err != nil {
		return err
	}
	i := slices.IndexFunc(listed.GetHosts(), func(h *pb.DeploymentHost) bool { return h.GetHostId() == host.HostID })
	if i < 0 {
		return fmt.Errorf("主机 %s 已不在 Admin 拓扑里", host.HostID)
	}
	current := listed.GetHosts()[i]
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "SyncHostDeployments", &pb.SyncHostDeploymentsReq{
		HostId: current.GetHostId(), Label: current.GetLabel(), Address: current.GetAddress(), PrivateAddress: current.GetPrivateAddress(),
		Region: current.GetRegion(), Reachability: current.GetReachability(), Description: current.GetDescription(),
	}, sync); err != nil {
		return err
	}
	if err := remoteRet("SyncHostDeployments", sync.GetRetInfo()); err != nil {
		return err
	}
	deleted := &pb.DeleteDeploymentHostRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "DeleteHost", &pb.DeleteDeploymentHostReq{HostId: host.HostID}, deleted); err != nil {
		return err
	}
	if err := remoteRet("DeleteHost", deleted.GetRetInfo()); err != nil {
		return err
	}
	return nil
}

// remoteRet 把 Admin 的错误码与说明带出来（这些说明不含配置值）。
func remoteRet(method string, ret *commonpb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty response", method)
	}
	if ret.GetCode() == commonpb.ErrorCode_SUCCESS {
		return nil
	}
	msg := strings.TrimSpace(ret.GetMsg())
	if msg == "" {
		msg = ret.GetCode().String()
	}
	return errors.New(method + ": " + msg)
}
