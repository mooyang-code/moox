package doctor

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/mooyang-code/moox/packages/servicecatalog"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

type Client struct {
	gateway gatewayclient.Invoker
}

// New borrows the command-owned gateway for both Monitor and SysDeploy.
func New(gateway gatewayclient.Invoker) *Client {
	return &Client{gateway: gateway}
}

func (c *Client) GetDoctorContext(ctx context.Context, req *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error) {
	if c == nil || c.gateway == nil {
		return nil, fmt.Errorf("monitor client is unavailable")
	}
	rsp := &monitorpb.GetDoctorContextRsp{}
	err := c.gateway.Invoke(ctx, "trpc.moox.monitor.MonitorMgr", "GetDoctorContext", req, rsp)
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo() == nil {
		return nil, fmt.Errorf("Monitor GetDoctorContext returned no status")
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("Monitor GetDoctorContext failed: %s", rsp.GetRetInfo().GetMsg())
	}
	return rsp, nil
}

// GetHealthOverview 只读取 Monitor 的健康概览；spaceID 为空时看全部空间。
func (c *Client) GetHealthOverview(ctx context.Context, spaceID string) (*monitorpb.HealthOverview, error) {
	if c == nil || c.gateway == nil {
		return nil, fmt.Errorf("Monitor 客户端不可用")
	}
	rsp := &monitorpb.GetHealthOverviewRsp{}
	if err := c.gateway.Invoke(ctx, "trpc.moox.monitor.MonitorMgr", "GetHealthOverview", &monitorpb.GetHealthOverviewReq{SpaceId: spaceID}, rsp); err != nil {
		return nil, err
	}
	if rsp.GetRetInfo() == nil {
		return nil, fmt.Errorf("Monitor GetHealthOverview 没有返回状态")
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("Monitor GetHealthOverview 失败：%s", rsp.GetRetInfo().GetMsg())
	}
	if rsp.GetOverview() == nil {
		return nil, fmt.Errorf("Monitor 返回了空的健康概览")
	}
	return rsp.GetOverview(), nil
}

// Deployment contains catalog-derived health information for one canonical component.
type Deployment struct {
	HostID        string
	ComponentID   string
	Status        string
	HealthAddress string
}

func (c *Client) ListDeployments(ctx context.Context, hostID string) ([]Deployment, error) {
	if c == nil || c.gateway == nil {
		return nil, fmt.Errorf("SysDeploy client is unavailable")
	}
	if !servicecatalog.ValidHostID(hostID) {
		return nil, fmt.Errorf("invalid deployment host")
	}
	hosts := &adminpb.ListDeploymentHostsRsp{}
	if err := c.gateway.Invoke(ctx, "trpc.moox.ops.SysDeploy", "ListHosts", &adminpb.ListDeploymentHostsReq{HostId: hostID, Page: &commonpb.Page{Page: 1, Size: 100}}, hosts); err != nil {
		return nil, err
	}
	if hosts.GetRetInfo() == nil || hosts.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("SysDeploy host query failed")
	}
	if len(hosts.GetHosts()) != 1 || hosts.GetHosts()[0].GetHostId() != hostID {
		return nil, fmt.Errorf("SysDeploy host inventory is inconsistent")
	}
	host := hosts.GetHosts()[0]
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	const pageSize = 100
	const maxRows = 500
	rows := make([]Deployment, 0)
	seen := map[string]bool{}
	for page := uint32(1); page <= maxRows/pageSize; page++ {
		rsp := &adminpb.ListDeploymentsRsp{}
		err := c.gateway.Invoke(ctx, "trpc.moox.ops.SysDeploy", "ListDeployments", &adminpb.ListDeploymentsReq{HostId: hostID, Page: &commonpb.Page{Page: page, Size: pageSize}}, rsp)
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("SysDeploy deployments query failed")
		}
		if len(rows)+len(rsp.GetDeployments()) > maxRows {
			return nil, fmt.Errorf("SysDeploy response exceeds %d rows", maxRows)
		}
		for _, deployment := range rsp.GetDeployments() {
			component, exists := catalog.Component(deployment.GetComponentId())
			if deployment.GetHostId() != hostID || !exists || seen[deployment.GetComponentId()] || (deployment.GetStatus() != servicecatalog.Enabled && deployment.GetStatus() != servicecatalog.Disabled) {
				return nil, fmt.Errorf("SysDeploy deployments inventory is inconsistent")
			}
			seen[deployment.GetComponentId()] = true
			status := deployment.GetStatus()
			if host.GetStatus() != servicecatalog.Enabled {
				status = servicecatalog.Disabled
			}
			address := host.GetAddress()
			if component.Health.Loopback {
				address = "127.0.0.1"
			}
			healthAddress := ""
			if component.Health.Port != 0 {
				healthAddress = "http://" + net.JoinHostPort(address, strconv.Itoa(component.Health.Port))
			}
			rows = append(rows, Deployment{HostID: hostID, ComponentID: component.ID, Status: status, HealthAddress: healthAddress})
		}
		if !rsp.GetPageResult().GetHasMore() {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("SysDeploy response exceeds %d rows", maxRows)
}
