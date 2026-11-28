package replay

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"

	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
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

// tunedDualClient 是现货、永续两个调参任务的结果 View，列与 tunedComputeClient 相同。
type tunedDualClient struct{ *dualClient }

func (c tunedDualClient) GetView(ctx context.Context, spaceID, datasetID string) (upstream.ViewInfo, error) {
	return tunedComputeClient{c.byDataset(datasetID)}.GetView(ctx, spaceID, datasetID)
}

func (c tunedDualClient) GetFactor(ctx context.Context, factorID string) (upstream.FactorInfo, error) {
	return tunedComputeClient{}.GetFactor(ctx, factorID)
}

const tunedDualDSL = `name: tuned_dual
market: {select: both, prefer: spot}
factor_params:
  f__var:
    window:
      default: "p"
rules:
  - id: r
    type: rank
    score: "m"
    filter: 'params("f__var", "window") > 0'
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

// 现货加永续、带 factor_params 的策略能现算回放：每个任务一个会话（ID 加市场后缀），两边每步收到同一份参数、同步推进；
// 生效参数按会话分别记录再合并：永续拒绝了某一期的参数时，记录取现货的那份，永续的拒绝与参数不同都写进这一期的说明。
func TestComputeDualTasksWithFactorParams(t *testing.T) {
	dual := newDualClient()
	client := tunedDualClient{dual}
	schedule := tunedSchedule(map[int]float64{0: 10, 1: 30}, nil)
	spotEngine := &fakeEngine{data: dual.spot, tuned: schedule, factors: tunedFactors}
	swapEngine := &fakeEngine{data: dual.swap, tuned: schedule, factors: tunedFactors, reject: func(window float64) string {
		if window == 30 {
			return "参数 window 不满足联合约束"
		}
		return ""
	}}
	engine := &sessionEngine{engines: map[string]*fakeEngine{"d1#spot": spotEngine, "d1#swap": swapEngine}}
	repo := openStore(t)
	job := startMarketReplay(t, repo, client, "d1", tunedDualDSL, 4, map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}, 10, 5)
	job.Input = store.ReplayInputCompute
	(&Runner{Store: repo, Client: client, Engine: engine, ChunkBars: 50, InitialEquity: 100}).Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "d1")
	require.NoError(t, err)
	require.Equal(t, store.ReplaySucceeded, done.Status, done.Error)
	bars := barsOf(t, repo, "d1")
	require.Len(t, bars, 4)

	for _, market := range []*fakeEngine{spotEngine, swapEngine} {
		require.Len(t, market.steps, 4, "声明了 factor_params 时每个会话每步一期")
		require.Equal(t, 1, market.closed, "结束时每个会话都关")
		require.Equal(t, []float64{0, 10, 30, 0}, []float64{requested(market, 0), requested(market, 1), requested(market, 2), requested(market, 3)}, "两个会话收到同一份参数")
	}
	require.Equal(t, []float64{0, 10, 30, 30}, []float64{paramsWindow(t, bars[0]), paramsWindow(t, bars[1]), paramsWindow(t, bars[2]), paramsWindow(t, bars[3])}, "记录取先到的现货会话生效的参数")
	var summary struct {
		Decision struct {
			Notes []string `json:"notes"`
		} `json:"decision"`
	}
	require.NoError(t, json.Unmarshal(bars[2].SummaryJSON, &summary))
	notes := strings.Join(summary.Decision.Notes, "\n")
	require.Contains(t, notes, "因子引擎拒绝了永续成员 f__var 这一期的参数（参数 window 不满足联合约束）")
	require.Contains(t, notes, "永续生效的参数与现货不同")
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
