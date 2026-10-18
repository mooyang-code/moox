package tradeowner

import (
	"context"
	"errors"
	"strings"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

// tradeService 是 Trade 账户所有权方法所在的 tRPC 服务。
const tradeService = "trpc.moox.trade.TradeConsoleService"

// gatewayProxy 经共享的网关客户端调用 Trade（目录发现、签名与重试都在其中），空间取自 ctx 里的调用元数据。
type gatewayProxy struct{ gateway gatewayclient.Invoker }

func (g *gatewayProxy) invoke(ctx context.Context, method string, request, response any, options []client.Option) error {
	if g == nil || g.gateway == nil {
		return configError{errors.New("Trade 网关客户端未配置")}
	}
	if len(options) != 0 {
		return configError{errors.New("Trade 网关调用由 ctx 与服务目录决定参数，不接受 tRPC 客户端选项")}
	}
	if strings.TrimSpace(gatewayclient.CallMetadataFromContext(ctx).SpaceID) == "" {
		return configError{errors.New("Trade 网关调用需要空间 ID")}
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

func (g *gatewayProxy) ClaimLogicalAccountOwner(ctx context.Context, req *tradepb.ClaimLogicalAccountOwnerReq, options ...client.Option) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.ClaimLogicalAccountOwnerRsp)
	if err := g.invoke(ctx, "ClaimLogicalAccountOwner", req, rsp, options); err != nil {
		return nil, err
	}
	return rsp, nil
}

func (g *gatewayProxy) ReleaseLogicalAccountOwner(ctx context.Context, req *tradepb.ReleaseLogicalAccountOwnerReq, options ...client.Option) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.ReleaseLogicalAccountOwnerRsp)
	if err := g.invoke(ctx, "ReleaseLogicalAccountOwner", req, rsp, options); err != nil {
		return nil, err
	}
	return rsp, nil
}

// visibleError 标记不含远端地址、可以原样返回给调用方的错误：配置或调用前提缺失。其余错误（连接、TLS 等）可能带有
// 网关地址，由 Client 包装为 TransportError，原始错误留给日志。
type visibleError interface{ visibleToCaller() }

// configError 是网关配置或调用前提缺失。
type configError struct{ error }

func (e configError) Unwrap() error  { return e.error }
func (configError) visibleToCaller() {}
