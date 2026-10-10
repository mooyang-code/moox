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

// Placement contains catalog-derived health information for one canonical component.
type Placement struct {
	HostID        string
	ComponentID   string
	Status        string
	HealthAddress string
}

func (c *Client) ListPlacements(ctx context.Context, hostID string) ([]Placement, error) {
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
	rows := make([]Placement, 0)
	seen := map[string]bool{}
	for page := uint32(1); page <= maxRows/pageSize; page++ {
		rsp := &adminpb.ListPlacementsRsp{}
		err := c.gateway.Invoke(ctx, "trpc.moox.ops.SysDeploy", "ListPlacements", &adminpb.ListPlacementsReq{HostId: hostID, Page: &commonpb.Page{Page: page, Size: pageSize}}, rsp)
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("SysDeploy placements query failed")
		}
		if len(rows)+len(rsp.GetPlacements()) > maxRows {
			return nil, fmt.Errorf("SysDeploy response exceeds %d rows", maxRows)
		}
		for _, placement := range rsp.GetPlacements() {
			component, exists := catalog.Component(placement.GetComponentId())
			if placement.GetHostId() != hostID || !exists || seen[placement.GetComponentId()] || (placement.GetStatus() != servicecatalog.Enabled && placement.GetStatus() != servicecatalog.Disabled) {
				return nil, fmt.Errorf("SysDeploy placements inventory is inconsistent")
			}
			seen[placement.GetComponentId()] = true
			status := placement.GetStatus()
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
			rows = append(rows, Placement{HostID: hostID, ComponentID: component.ID, Status: status, HealthAddress: healthAddress})
		}
		if !rsp.GetPageResult().GetHasMore() {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("SysDeploy response exceeds %d rows", maxRows)
}
