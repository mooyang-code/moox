package doctor

import (
	"context"
	"fmt"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

// Client 读取 Monitor 的诊断上下文和 SysDeploy 的部署记录。
type Client struct {
	monitor   monitorpb.MonitorMgrClientProxy
	sysdeploy adminpb.SysDeployClientProxy
}

// New 用给定的 tRPC 客户端选项（gatewayclient 的 ClientOptions）创建客户端。
func New(options []client.Option) *Client {
	return &Client{
		monitor:   monitorpb.NewMonitorMgrClientProxy(options...),
		sysdeploy: adminpb.NewSysDeployClientProxy(options...),
	}
}

func (c *Client) GetDoctorContext(ctx context.Context, req *monitorpb.GetDoctorContextReq) (*monitorpb.GetDoctorContextRsp, error) {
	if c == nil || c.monitor == nil {
		return nil, fmt.Errorf("monitor client is unavailable")
	}
	rsp, err := c.monitor.GetDoctorContext(ctx, req)
	if err != nil {
		return nil, err
	}
	if rsp == nil {
		return nil, fmt.Errorf("Monitor GetDoctorContext returned an empty response")
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("Monitor GetDoctorContext failed: %s", rsp.GetRetInfo().GetMsg())
	}
	return rsp, nil
}

func (c *Client) ListDeployments(ctx context.Context, nodeID string) ([]*adminpb.ServiceDeployment, error) {
	if c == nil || c.sysdeploy == nil {
		return nil, fmt.Errorf("SysDeploy client is unavailable")
	}
	const pageSize = 100
	const maxRows = 500
	rows := make([]*adminpb.ServiceDeployment, 0, pageSize)
	for page := uint32(1); page <= maxRows/pageSize; page++ {
		rsp, err := c.sysdeploy.ListServiceDeployments(ctx, &adminpb.ListServiceDeploymentsReq{NodeId: nodeID, Page: &commonpb.Page{Page: page, Size: pageSize}})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
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
