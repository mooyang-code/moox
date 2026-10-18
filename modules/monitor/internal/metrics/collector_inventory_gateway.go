package metrics

import (
	"context"
	"errors"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

// CollectorInventoryGatewayClient borrows the process-owned gateway client.
type CollectorInventoryGatewayClient struct{ gateway gatewayclient.Invoker }

func NewCollectorInventoryGatewayClient(gateway gatewayclient.Invoker) (*CollectorInventoryGatewayClient, error) {
	if gateway == nil {
		return nil, errors.New("collector inventory gateway client is required")
	}
	return &CollectorInventoryGatewayClient{gateway: gateway}, nil
}

func (c *CollectorInventoryGatewayClient) GetTaskResultInventory(ctx context.Context, req *collectorpb.GetTaskResultInventoryReq, options ...client.Option) (*collectorpb.GetTaskResultInventoryRsp, error) {
	if c == nil || c.gateway == nil || len(options) != 0 {
		return nil, errors.New("collector inventory requires the shared gateway without RPC overrides")
	}
	rsp := &collectorpb.GetTaskResultInventoryRsp{}
	if err := c.gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "GetTaskResultInventory", req, rsp); err != nil {
		return nil, err
	}
	return rsp, nil
}
