package replay

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/stretchr/testify/require"
)

// signal 改写对照（批 3 T0 第 3 项）：金样 swap_universe_pool.json 的策略里有一条显式 pool 的 signal 规则，按标准写法
// （rank 加 hold，不带 allow_empty）改写后用同一份输入重跑研究回放，逐期比较结果状态、跳过原因、目标、账本与权益，以及最终绩效。
// 摘要（summary）里有规则的求值计数与明细原因，两种写法本来就不同，不比。改写后的输出另存为 swap_universe_pool_rank.json：
// signal 删除之后原金样不能再生成，由这一份接替。

// baselineSwapRankDSL 是 baselineSwapDSL 的标准改写：只有规则 e 从 signal 换成 rank 加 hold，其余逐字相同。
var baselineSwapRankDSL = strings.Replace(baselineSwapDSL, `  - id: e
    type: signal
    pool: [E]
    entry: "mom > 0"
    exit: "mom < 0"
    weight: {total: 0.5}
`, `  - id: e
    type: rank
    pool: [E]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "mom > 0"
      exit: "mom < 0"
    weight: {total: 0.5}
`, 1)

func TestSignalRewriteReplayMatchesBaseline(t *testing.T) {
	require.NotEqual(t, baselineSwapDSL, baselineSwapRankDSL, "改写没有生效")
	require.NotContains(t, baselineSwapRankDSL, "signal")
	scenario := func(name, raw string) baselineReplayScenario {
		return baselineReplayScenario{name: name, dsl: raw, hours: 16,
			client: func() input.Client { return newBaselineClient(baselineSwapClient()) },
			tasks:  map[string]store.ReplayTask{"swap": swapTask}, check: baselineCheckSwap}
	}
	signalDump, signal := baselineReplayDump(t, scenario("swap_universe_pool", baselineSwapDSL))
	if !baselineUpdate() {
		// 比较的基准就是现有金样。
		baselineCompare(t, filepath.Join("testdata", "baseline", "swap_universe_pool.json"), baselineJSON(t, signalDump))
	}
	rankDump, rank := baselineReplayDump(t, scenario("swap_universe_pool_rank", baselineSwapRankDSL))
	baselineCheckSwap(t, rank)
	require.Len(t, rank, len(signal))
	for i := range signal {
		want, got := signal[i], rank[i]
		require.Equal(t, want.BarEndTime.Unix(), got.BarEndTime.Unix(), "第 %d 期", i)
		require.Equal(t, want.Status, got.Status, "第 %d 期的结果状态", i)
		require.Equal(t, want.SkipReason, got.SkipReason, "第 %d 期的跳过原因", i)
		require.Equal(t, string(want.TargetsJSON), string(got.TargetsJSON), "第 %d 期的目标", i)
		require.Equal(t, string(want.PositionsJSON), string(got.PositionsJSON), "第 %d 期的账本", i)
		require.Equal(t, []float64{want.Return, want.Equity, want.Turnover, want.Fee, want.SpotValue, want.SwapValue},
			[]float64{got.Return, got.Equity, got.Turnover, got.Fee, got.SpotValue, got.SwapValue}, "第 %d 期的收益、权益、换手、费用与市值", i)
		require.Equal(t, []int{want.Holdings, want.Frozen, want.Unfilled, want.Liquidated},
			[]int{got.Holdings, got.Frozen, got.Unfilled, got.Liquidated}, "第 %d 期的持仓、冻结、未成交与清算个数", i)
	}
	require.Equal(t, signalDump["metrics"], rankDump["metrics"], "最终绩效")
	baselineCompare(t, filepath.Join("testdata", "baseline", "swap_universe_pool_rank.json"), baselineJSON(t, rankDump))
}
