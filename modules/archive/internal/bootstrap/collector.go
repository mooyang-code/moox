package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// gatewayCollector 经网关调用 Collector 的 CollectMgr.GetCollectorStatus，取空间采集函数服务的频率。
type gatewayCollector struct {
	gateway gatewayclient.Invoker
}

func newGatewayCollector(gateway gatewayclient.Invoker) *gatewayCollector {
	return &gatewayCollector{gateway: gateway}
}

// SpaceFrequencies 返回空间全部采集函数（已生效、下发中、退役中都算：它们的定时器都可能在触发）服务的频率。
func (g *gatewayCollector) SpaceFrequencies(ctx context.Context, spaceID string) ([]string, error) {
	if g == nil || g.gateway == nil {
		return nil, errors.New("归档读取采集频率需要网关客户端")
	}
	rsp := &collectorpb.GetCollectorStatusRsp{}
	if err := g.gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "GetCollectorStatus", &collectorpb.GetCollectorStatusReq{SpaceId: spaceID}, rsp); err != nil {
		return nil, err
	}
	if code := rsp.GetRetInfo().GetCode(); code != collectorpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("GetCollectorStatus 返回 %v：%s", code, rsp.GetRetInfo().GetMsg())
	}
	return functionFrequencies(rsp.GetFunctions()), nil
}

// functionFrequencies 从函数服务的“数据集|频率”里取出频率（不去重，由调用方处理）。
func functionFrequencies(functions []*collectorpb.CollectorFunctionStatus) []string {
	var out []string
	for _, fn := range functions {
		for _, entry := range fn.GetDatasets() {
			if i := strings.LastIndex(entry, "|"); i >= 0 && i < len(entry)-1 {
				out = append(out, strings.TrimSpace(entry[i+1:]))
			}
		}
	}
	return out
}
