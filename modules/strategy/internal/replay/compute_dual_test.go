package replay

import (
	"context"
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// sessionEngine 模拟因子引擎同时开多个会话：按会话 ID 路由到各自的 fakeEngine（每个市场一个）。
type sessionEngine struct {
	engines map[string]*fakeEngine
}

func (e *sessionEngine) OpenReplay(ctx context.Context, req *factorpb.OpenReplayReq, opts ...client.Option) (*factorpb.OpenReplayRsp, error) {
	return e.engines[req.GetReplayId()].OpenReplay(ctx, req, opts...)
}

func (e *sessionEngine) StepReplay(ctx context.Context, req *factorpb.StepReplayReq, opts ...client.Option) (*factorpb.StepReplayRsp, error) {
	return e.engines[req.GetReplayId()].StepReplay(ctx, req, opts...)
}

func (e *sessionEngine) CloseReplay(ctx context.Context, req *factorpb.CloseReplayReq, opts ...client.Option) (*factorpb.CloseReplayRsp, error) {
	return e.engines[req.GetReplayId()].CloseReplay(ctx, req, opts...)
}

// 第二个会话建不起来时，已建的第一个会话也关掉，回放失败。
func TestComputeDualTasksClosesFirstSessionWhenSecondFails(t *testing.T) {
	dual := newDualClient()
	spotEngine := &fakeEngine{data: dual.spot}
	swapEngine := &fakeEngine{data: dual.swap, openErr: context.DeadlineExceeded}
	engine := &sessionEngine{engines: map[string]*fakeEngine{"d2#spot": spotEngine, "d2#swap": swapEngine}}
	repo := openStore(t)
	job := startMarketReplay(t, repo, dual, "d2", hedgeDSL, 4, map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}, 10, 5)
	job.Input = store.ReplayInputCompute
	(&Runner{Store: repo, Client: dual, Engine: engine, ChunkBars: 50, InitialEquity: 100}).Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "d2")
	require.NoError(t, err)
	require.Equal(t, store.ReplayFailed, done.Status)
	require.Contains(t, done.Error, "因子引擎不在线")
	require.Equal(t, []string{"d2#spot"}, spotEngine.closedIDs, "已建的现货会话要关")
	require.Equal(t, []string{"d2#swap"}, swapEngine.closedIDs, "结果未知的永续会话也尽力关一次")
}
