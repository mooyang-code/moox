package localexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	cloudfunctionpb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// ServiceName 是本地执行端的内部服务。
const ServiceName = "trpc.moox.collector.MarketFetchExecutor"

// Client 是 Collector 调用本地执行端的客户端，与 scfinvoker 的同步调用返回同样的结果，补采据此不区分执行端。
type Client struct {
	Gateway gatewayclient.Invoker
}

// Invoke 把事件交给本地执行端执行；nodeID 只用于日志（本地执行端不需要建函数）。
func (c *Client) Invoke(ctx context.Context, spaceID, _ string, event map[string]any, _ cloudfunctionpb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	if c == nil || c.Gateway == nil {
		return scfinvoker.InvocationResult{}, errors.New("local executor client requires the process gateway client")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return scfinvoker.InvocationResult{}, err
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = spaceID
	var rsp pb.InvokeMarketFetchRsp
	if err := c.Gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), ServiceName, "Invoke", &pb.InvokeMarketFetchReq{SpaceId: spaceID, Event: raw}, &rsp); err != nil {
		return scfinvoker.InvocationResult{}, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return scfinvoker.InvocationResult{}, fmt.Errorf("local executor: %s", rsp.GetRetInfo().GetMsg())
	}
	result := map[string]any{}
	if err := json.Unmarshal(rsp.GetResponse(), &result); err != nil {
		return scfinvoker.InvocationResult{}, fmt.Errorf("decode local executor response: %w", err)
	}
	message, _ := result["message"].(string)
	requestID, _ := result["request_id"].(string)
	return scfinvoker.InvocationResult{RequestID: requestID, Message: message, Result: result}, nil
}
