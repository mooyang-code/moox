package factorio

import (
	"context"
	"errors"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

type gatewayAdapter struct{ gateway gatewayclient.Invoker }

var (
	_ MetadataClient = (*gatewayAdapter)(nil)
)

// NewGatewayClient uses the shared process client for discovery, signing and retries.
func NewGatewayClient(gateway gatewayclient.Invoker) *RPCClient {
	adapter := &gatewayAdapter{gateway: gateway}
	return &RPCClient{Proxy: adapter}
}

func invokeGateway[T any](ctx context.Context, gateway gatewayclient.Invoker, service, method string, request any, options []client.Option) (*T, error) {
	if gateway == nil {
		return nil, errors.New("strategy gateway client is required")
	}
	if len(options) != 0 {
		return nil, errors.New("strategy gateway calls use context and catalog settings instead of tRPC client options")
	}
	response := new(T)
	if err := gateway.Invoke(ctx, service, method, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *gatewayAdapter) ListFactorSets(ctx context.Context, request *factorpb.ListFactorSetsReq, options ...client.Option) (*factorpb.ListFactorSetsRsp, error) {
	return invokeGateway[factorpb.ListFactorSetsRsp](ctx, c.gateway, "trpc.moox.factor.FactorMgr", "ListFactorSets", request, options)
}

func (c *gatewayAdapter) ListFactors(ctx context.Context, request *factorpb.ListFactorsReq, options ...client.Option) (*factorpb.ListFactorsRsp, error) {
	return invokeGateway[factorpb.ListFactorsRsp](ctx, c.gateway, "trpc.moox.factor.FactorMgr", "ListFactors", request, options)
}
