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
	// SSHRecords 是控制台 SSH 终端里与主机 ID 同名的记录数。
	SSHRecords int `json:"ssh_records"`
}

// RegisteredHostByID 读出 Admin 拓扑里的主机与它的组件放置；没有登记时返回 found=false。
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
	placements := &pb.ListPlacementsRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "ListPlacements", &pb.ListPlacementsReq{HostId: hostID}, placements); err != nil {
		return RegisteredHost{}, false, err
	}
	if err := remoteRet("ListPlacements", placements.GetRetInfo()); err != nil {
		return RegisteredHost{}, false, err
	}
	for _, p := range placements.GetPlacements() {
		if p.GetHostId() == hostID {
			host.Components = append(host.Components, p.GetComponentId())
		}
	}
	slices.Sort(host.Components)
	ids, err := c.sshRecordIDs(ctx, hostID)
	if err != nil {
		return RegisteredHost{}, false, err
	}
	host.SSHRecords = len(ids)
	return host, true, nil
}

// RemoveRegisteredHost 从 Admin 拓扑删除主机：先清空业务组件放置（网关路由随之撤销），再删除主机，
// 最后删除控制台 SSH 终端里同名的记录。主机删除后它的网关签名身份不再被任何网关接受。
func (c *Client) RemoveRegisteredHost(ctx context.Context, host RegisteredHost) error {
	sync := &pb.SyncHostPlacementsRsp{}
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
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "SyncHostPlacements", &pb.SyncHostPlacementsReq{
		HostId: current.GetHostId(), Address: current.GetAddress(), PrivateAddress: current.GetPrivateAddress(),
		Region: current.GetRegion(), Reachability: current.GetReachability(), Description: current.GetDescription(),
	}, sync); err != nil {
		return err
	}
	if err := remoteRet("SyncHostPlacements", sync.GetRetInfo()); err != nil {
		return err
	}
	deleted := &pb.DeleteDeploymentHostRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.SysDeploy", "DeleteHost", &pb.DeleteDeploymentHostReq{HostId: host.HostID}, deleted); err != nil {
		return err
	}
	if err := remoteRet("DeleteHost", deleted.GetRetInfo()); err != nil {
		return err
	}
	ids, err := c.sshRecordIDs(ctx, host.HostID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		rsp := &pb.DeleteHostRsp{}
		if err := c.invoke(ctx, "trpc.moox.ops.Ssh", "DeleteHost", &pb.DeleteHostReq{Id: id}, rsp); err != nil {
			return err
		}
		if err := remoteRet("Ssh.DeleteHost", rsp.GetRetInfo()); err != nil {
			return err
		}
	}
	return nil
}

// sshRecordIDs 返回控制台 SSH 终端里名称等于主机 ID 的记录 ID；响应里的登录凭据立即丢弃。
func (c *Client) sshRecordIDs(ctx context.Context, hostID string) ([]int32, error) {
	rsp := &pb.ListHostsRsp{}
	if err := c.invoke(ctx, "trpc.moox.ops.Ssh", "ListHosts", &pb.ListHostsReq{Keyword: hostID, Limit: 100}, rsp); err != nil {
		return nil, err
	}
	if err := remoteRet("Ssh.ListHosts", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	var ids []int32
	for _, host := range rsp.GetHosts() {
		if host.GetName() == hostID {
			ids = append(ids, host.GetId())
		}
		host.Password = ""
	}
	return ids, nil
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
