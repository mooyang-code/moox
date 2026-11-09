package rpc

import (
	"testing"

	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// 单策略回放的 funding_column：永续策略记下列名；列名非法、策略不涉及永续时拒绝；策略池写了时提示改写池 DSL 的 fees.funding_column。
func TestStartReplayFundingColumn(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	started, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"swap": "ftask_aswap"}, StartTime: "2026-09-02T00:00:00Z", EndTime: "2026-09-03T00:00:00Z", FundingColumn: " funding_rate "})
	requireOK(t, int32(started.GetRetInfo().GetCode()), started.GetRetInfo().GetMsg())
	stored, err := h.service.Store.GetReplay(h.ctx, started.GetReplay().GetReplayId())
	if err != nil || stored.FundingColumn != "funding_rate" || started.GetReplay().GetFundingColumn() != "funding_rate" {
		t.Fatalf("回放应记下资金费列：%+v %v", stored, err)
	}
	plain, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"swap": "ftask_aswap"}, StartTime: "2026-09-02T00:00:00Z", EndTime: "2026-09-03T00:00:00Z"})
	requireOK(t, int32(plain.GetRetInfo().GetCode()), plain.GetRetInfo().GetMsg())
	if plain.GetReplay().GetFundingColumn() != "" {
		t.Fatalf("不写时为空：%+v", plain.GetReplay())
	}
	invalid, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"swap": "ftask_aswap"}, StartTime: "2026-09-02T00:00:00Z", EndTime: "2026-09-03T00:00:00Z", FundingColumn: "funding-rate"})
	requireFail(t, int32(invalid.GetRetInfo().GetCode()), invalid.GetRetInfo().GetMsg(), "必须是 View 里的列名")
	spot, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: "2026-09-02T00:00:00Z", EndTime: "2026-09-03T00:00:00Z", FundingColumn: "funding_rate"})
	requireFail(t, int32(spot.GetRetInfo().GetCode()), spot.GetRetInfo().GetMsg(), "不涉及永续")

	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("pool_1", poolDefDSL)
	pooled, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "pool_1", FactorTasks: map[string]string{"spot": "ftask_p2", "swap": "ftask_p2swap"}, StartTime: "2026-09-01", EndTime: "2026-09-02", FundingColumn: "funding_rate"})
	requireFail(t, int32(pooled.GetRetInfo().GetCode()), pooled.GetRetInfo().GetMsg(), "strategy_pool.fees.funding_column")
}
