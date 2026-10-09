package doctor

import (
	"context"
	"fmt"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

// Client 读取 Monitor 的诊断上下文和 SysDeploy 的部署。
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

// ListPlacements 返回一台主机上的全部部署。
func (c *Client) ListPlacements(ctx context.Context, hostID string) ([]*adminpb.DeployPlacement, error) {
	if c == nil || c.sysdeploy == nil {
		return nil, fmt.Errorf("SysDeploy 客户端不可用")
	}
	rsp, err := c.sysdeploy.ListPlacements(ctx, &adminpb.ListPlacementsReq{HostId: hostID})
	if err != nil {
		return nil, err
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("读取主机 %s 的部署失败（%s）: %s", hostID, code, rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetPlacements(), nil
}
