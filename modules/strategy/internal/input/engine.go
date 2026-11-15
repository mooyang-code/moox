package input

import (
	"context"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

// EngineClient 是回放“因子现算”来源调用因子引擎入站服务 EngineControl 的最小方法集合，便于测试替换。
type EngineClient interface {
	OpenReplay(context.Context, *factorpb.OpenReplayReq, ...client.Option) (*factorpb.OpenReplayRsp, error)
	StepReplay(context.Context, *factorpb.StepReplayReq, ...client.Option) (*factorpb.StepReplayRsp, error)
	CloseReplay(context.Context, *factorpb.CloseReplayReq, ...client.Option) (*factorpb.CloseReplayRsp, error)
}

// factorEngineService 是因子引擎的入站服务：只监听因子机本机，经因子机的主机网关转发（因子机只出站时经控制机中转），
// 寻址与中转由网关客户端按服务目录完成。
const factorEngineService = "trpc.moox.factor.EngineControl"

// engineAdapter 经共享的网关客户端调用因子引擎。不套 factor.timeout（5 秒，给 FactorMgr 的短调用用）：
// 单次超时取服务目录里的路由超时（OpenReplay 要读整段本地快照，10 分钟；其余 2 分钟）。
type engineAdapter struct {
	gateway gatewayclient.Invoker
}

var _ EngineClient = engineAdapter{}

// NewEngineClient 构造经网关访问因子引擎 EngineControl 的客户端。
func NewEngineClient(gateway gatewayclient.Invoker) EngineClient {
	return engineAdapter{gateway: gateway}
}

func (a engineAdapter) OpenReplay(ctx context.Context, request *factorpb.OpenReplayReq, options ...client.Option) (*factorpb.OpenReplayRsp, error) {
	return invokeGateway[factorpb.OpenReplayRsp](ctx, a.gateway, 0, factorEngineService, "OpenReplay", request, options)
}

func (a engineAdapter) StepReplay(ctx context.Context, request *factorpb.StepReplayReq, options ...client.Option) (*factorpb.StepReplayRsp, error) {
	return invokeGateway[factorpb.StepReplayRsp](ctx, a.gateway, 0, factorEngineService, "StepReplay", request, options)
}

func (a engineAdapter) CloseReplay(ctx context.Context, request *factorpb.CloseReplayReq, options ...client.Option) (*factorpb.CloseReplayRsp, error) {
	return invokeGateway[factorpb.CloseReplayRsp](ctx, a.gateway, 0, factorEngineService, "CloseReplay", request, options)
}
