package tradeowner

import (
	"context"
	"errors"
	"strings"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

// tradeService 是 Trade 组合账户方法所在的 tRPC 服务。
const tradeService = "trpc.moox.trade.TradeConsoleService"

// gatewayProxy 经共享的网关客户端调用 Trade（目录发现、签名与重试都在其中），空间取自 ctx 里的调用元数据。
type gatewayProxy struct{ gateway gatewayclient.Invoker }

func (g *gatewayProxy) invoke(ctx context.Context, method string, request, response any, options []client.Option) error {
	if g == nil || g.gateway == nil {
		return errors.New("Trade 网关客户端未配置")
	}
	if len(options) != 0 {
		return errors.New("Trade 网关调用由 ctx 与服务目录决定参数，不接受 tRPC 客户端选项")
	}
	if strings.TrimSpace(gatewayclient.CallMetadataFromContext(ctx).SpaceID) == "" {
		return errors.New("Trade 网关调用需要空间 ID")
	}
	return g.gateway.Invoke(ctx, tradeService, method, request, response)
}

func (g *gatewayProxy) GetLogicalAccount(ctx context.Context, req *tradepb.GetLogicalAccountReq, options ...client.Option) (*tradepb.GetLogicalAccountRsp, error) {
	rsp := new(tradepb.GetLogicalAccountRsp)
	if err := g.invoke(ctx, "GetLogicalAccount", req, rsp, options); err != nil {
		return nil, err
	}
	return rsp, nil
}

func (g *gatewayProxy) CloseTargetSession(ctx context.Context, req *tradepb.CloseTargetSessionReq, options ...client.Option) (*tradepb.CloseTargetSessionRsp, error) {
	rsp := new(tradepb.CloseTargetSessionRsp)
	if err := g.invoke(ctx, "CloseTargetSession", req, rsp, options); err != nil {
		return nil, err
	}
	return rsp, nil
}
