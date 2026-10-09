package doctor

import (
	"context"
	"fmt"

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

func (c *Client) ListDeployments(ctx context.Context, nodeID string) ([]*adminpb.ServiceDeployment, error) {
	if c == nil || c.gateway == nil {
		return nil, fmt.Errorf("SysDeploy client is unavailable")
	}
	const pageSize = 100
	const maxRows = 500
	rows := make([]*adminpb.ServiceDeployment, 0, pageSize)
	for page := uint32(1); page <= maxRows/pageSize; page++ {
		rsp := &adminpb.ListServiceDeploymentsRsp{}
		err := c.gateway.Invoke(ctx, "trpc.moox.ops.SysDeploy", "ListServiceDeployments", &adminpb.ListServiceDeploymentsReq{NodeId: nodeID, Page: &commonpb.Page{Page: page, Size: pageSize}}, rsp)
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("SysDeploy list failed: %s", rsp.GetRetInfo().GetMsg())
		}
		if len(rows)+len(rsp.GetDeployments()) > maxRows {
			return nil, fmt.Errorf("SysDeploy response exceeds %d rows", maxRows)
		}
		rows = append(rows, rsp.GetDeployments()...)
		if !rsp.GetPageResult().GetHasMore() {
			return rows, nil
		}
	}
	return nil, fmt.Errorf("SysDeploy response exceeds %d rows", maxRows)
}
