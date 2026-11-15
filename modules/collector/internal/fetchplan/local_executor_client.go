package fetchplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// localExecutorService 是本地执行端的内部服务。
const localExecutorService = "trpc.moox.collector.MarketFetchExecutor"

// LocalExecutorClient 是 Collector 调用本地执行端（collector-stream 里的 MarketFetchExecutor）的客户端，
// 与云函数的同步调用返回同样的结果，补采据此不区分执行端。
type LocalExecutorClient struct {
	Gateway gatewayclient.Invoker
}

// Invoke 把事件交给本地执行端执行；本地执行端不需要建函数，函数名不用。
func (c *LocalExecutorClient) Invoke(ctx context.Context, spaceID, _ string, event map[string]any) (marketfetch.InvocationResult, error) {
	if c == nil || c.Gateway == nil {
		return marketfetch.InvocationResult{}, errors.New("local executor client requires the process gateway client")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return marketfetch.InvocationResult{}, err
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = spaceID
	var rsp pb.InvokeMarketFetchRsp
	if err := c.Gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), localExecutorService, "Invoke", &pb.InvokeMarketFetchReq{SpaceId: spaceID, Event: raw}, &rsp); err != nil {
		return marketfetch.InvocationResult{}, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return marketfetch.InvocationResult{}, fmt.Errorf("local executor: %s", rsp.GetRetInfo().GetMsg())
	}
	result := map[string]any{}
	if err := json.Unmarshal(rsp.GetResponse(), &result); err != nil {
		return marketfetch.InvocationResult{}, fmt.Errorf("decode local executor response: %w", err)
	}
	message, _ := result["message"].(string)
	requestID, _ := result["request_id"].(string)
	return marketfetch.InvocationResult{RequestID: requestID, Message: message, Result: result}, nil
}
