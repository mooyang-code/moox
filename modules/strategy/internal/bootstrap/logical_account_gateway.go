package bootstrap

import (
	"context"
	"errors"
	"strings"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

type logicalAccountProxy interface {
	GetLogicalAccount(context.Context, *tradepb.GetLogicalAccountReq, ...client.Option) (*tradepb.GetLogicalAccountRsp, error)
	ClaimLogicalAccountOwner(context.Context, *tradepb.ClaimLogicalAccountOwnerReq, ...client.Option) (*tradepb.ClaimLogicalAccountOwnerRsp, error)
	ReleaseLogicalAccountOwner(context.Context, *tradepb.ReleaseLogicalAccountOwnerReq, ...client.Option) (*tradepb.ReleaseLogicalAccountOwnerRsp, error)
	RebindLogicalAccountOwner(context.Context, *tradepb.RebindLogicalAccountOwnerReq, ...client.Option) (*tradepb.RebindLogicalAccountOwnerRsp, error)
}

// The process owns this client; the account adapter only borrows it.
type logicalAccountGateway struct{ gateway gatewayclient.Invoker }

func newLogicalAccountGateway(gateway gatewayclient.Invoker) *logicalAccountGateway {
	return &logicalAccountGateway{gateway: gateway}
}

func (g *logicalAccountGateway) invoke(ctx context.Context, method string, request, response any, options []client.Option) error {
	if g == nil || g.gateway == nil {
		return errors.New("Trade gateway client is required")
	}
	if len(options) != 0 {
		return errors.New("Trade gateway calls use context and catalog settings instead of tRPC client options")
	}
	if strings.TrimSpace(gatewayclient.CallMetadataFromContext(ctx).SpaceID) == "" {
		return errors.New("Trade gateway space is required")
	}
	return g.gateway.Invoke(ctx, "trpc.moox.trade.TradeConsoleService", method, request, response)
}

func (g *logicalAccountGateway) GetLogicalAccount(ctx context.Context, req *tradepb.GetLogicalAccountReq, options ...client.Option) (*tradepb.GetLogicalAccountRsp, error) {
	rsp := new(tradepb.GetLogicalAccountRsp)
	return rsp, g.invoke(ctx, "GetLogicalAccount", req, rsp, options)
}

func (g *logicalAccountGateway) ClaimLogicalAccountOwner(ctx context.Context, req *tradepb.ClaimLogicalAccountOwnerReq, options ...client.Option) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.ClaimLogicalAccountOwnerRsp)
	return rsp, g.invoke(ctx, "ClaimLogicalAccountOwner", req, rsp, options)
}

func (g *logicalAccountGateway) ReleaseLogicalAccountOwner(ctx context.Context, req *tradepb.ReleaseLogicalAccountOwnerReq, options ...client.Option) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.ReleaseLogicalAccountOwnerRsp)
	return rsp, g.invoke(ctx, "ReleaseLogicalAccountOwner", req, rsp, options)
}

func (g *logicalAccountGateway) RebindLogicalAccountOwner(ctx context.Context, req *tradepb.RebindLogicalAccountOwnerReq, options ...client.Option) (*tradepb.RebindLogicalAccountOwnerRsp, error) {
	rsp := new(tradepb.RebindLogicalAccountOwnerRsp)
	return rsp, g.invoke(ctx, "RebindLogicalAccountOwner", req, rsp, options)
}
