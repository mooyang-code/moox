package dsl

import (
	"strings"
	"testing"
)

const poolHead = `name: 双策略轮动
bar: 1h
strategy_pool:
  members:
    - {id: a, strategy: strategy_a}
    - {id: b, strategy: strategy_b}
`

func parsePool(t *testing.T, body string) (Strategy, error) {
	t.Helper()
	return Parse([]byte(poolHead + body))
}

func TestParsePoolFixedAndRotation(t *testing.T) {
	t.Parallel()
	fixed, err := parsePool(t, `  allocation: {method: fixed, ratios: {a: 0.6, b: 0.4}}
  timing: {window: 24}
  step: 0.2
portfolio: {leverage: 1.5, max_weight: 0.1}
`)
	if err != nil {
		t.Fatal(err)
	}
	pool := fixed.Pool
	if !fixed.IsPool() || len(fixed.Rules) != 0 || len(pool.Members) != 2 || pool.Members[1].StrategyID != "strategy_b" {
		t.Fatalf("解析结果不符：%+v", fixed)
	}
	if pool.Allocation.Method != AllocationFixed || pool.Allocation.Ratios["a"].String() != "0.6" || pool.Timing.Window != 24 || !pool.HasStep || pool.Step.String() != "0.2" {
		t.Fatalf("分配参数不符：%+v", pool)
	}
	if pool.Fees.SpotBps != DefaultPoolSpotFeeBps || pool.Fees.SwapBps != DefaultPoolSwapFeeBps || pool.MaxWindow() != 24 {
		t.Fatalf("费率默认值或窗口不符：%+v", pool)
	}
	rotation, err := parsePool(t, `  allocation:
    method: rotation
    rotation:
      factors:
        - {name: return, window: 24, weight: 1}
        - {name: ma_ratio, window: 72, weight: 2}
        - {name: drawdown, window: 48, order: desc}
      top: 1
  fees: {spot_bps: 20, swap_bps: 2}
`)
	if err != nil {
		t.Fatal(err)
	}
	factors := rotation.Pool.Allocation.Rotation.Factors
	if len(factors) != 3 || factors[1].Weight.String() != "2" || rotation.Pool.MaxWindow() != 72 || rotation.Pool.Fees.SpotBps != 20 || rotation.Pool.Fees.SwapBps != 2 {
		t.Fatalf("轮动参数不符：%+v", rotation.Pool)
	}
	for i, want := range []string{OrderDesc, OrderDesc, OrderDesc} {
		if got := factors[i].EffectiveOrder(); got != want {
			t.Fatalf("因子 %d 的方向 %s，期望 %s", i, got, want)
		}
	}
	if (RotationFactor{Name: FactorDrawdown}).EffectiveOrder() != OrderAsc {
		t.Fatal("回撤默认越小越好")
	}
}

func TestParsePoolRejectsIllegal(t *testing.T) {
	t.Parallel()
	const okAllocation = "  allocation: {method: fixed, ratios: {a: 1, b: 0}}\n"
	cases := map[string]struct{ doc, contains string }{
		"同时写 rules":         {poolHead + okAllocation + "rules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\n", "只能写一个"},
		"池写 market":         {poolHead + okAllocation + "market: {select: spot}\n", "不写 market"},
		"池没有 bar":           {strings.Replace(poolHead, "bar: 1h\n", "", 1) + okAllocation, "必须写 bar"},
		"池写 universe":       {poolHead + okAllocation + "universe: {tags: [x]}\n", "不写 universe"},
		"池写 factor_params":  {poolHead + okAllocation + "factor_params:\n  m:\n    p: {default: \"1\"}\n", "factor_params"},
		"没有成员":              {"name: p\nbar: 1h\nstrategy_pool:\n  members: []\n" + okAllocation, "至少需要一个成员"},
		"成员 id 重复":          {strings.Replace(poolHead, "id: b", "id: a", 1) + "  allocation: {method: fixed, ratios: {a: 1}}\n", "重复"},
		"成员 id 非法":          {strings.Replace(poolHead, "id: b", "id: B1", 1) + okAllocation, "snake_case"},
		"成员缺 strategy":      {"name: p\nbar: 1h\nstrategy_pool:\n  members:\n    - {id: a}\n" + okAllocation, "strategy"},
		"没有 method":         {poolHead + "  allocation: {ratios: {a: 1}}\n", "必须写 fixed、rotation 或 drift"},
		"未知 method":         {poolHead + "  allocation: {method: equal}\n", "不受支持"},
		"fixed 缺比例":         {poolHead + "  allocation: {method: fixed, ratios: {a: 1}}\n", "缺少成员 b"},
		"fixed 多余成员":        {poolHead + "  allocation: {method: fixed, ratios: {a: 1, b: 1, c: 1}}\n", "不是池的成员"},
		"fixed 负比例":         {poolHead + "  allocation: {method: fixed, ratios: {a: -1, b: 1}}\n", "必须在 0 到"},
		"fixed 写 rotation":  {poolHead + "  allocation: {method: fixed, ratios: {a: 1, b: 1}, rotation: {top: 1}}\n", "不能写 rotation"},
		"rotation 写 ratios": {poolHead + "  allocation: {method: rotation, ratios: {a: 1, b: 1}}\n", "不能写 ratios"},
		"rotation 没因子":      {poolHead + "  allocation: {method: rotation, rotation: {top: 1, factors: []}}\n", "需要写 rotation.factors"},
		"top 超过成员数":         {poolHead + "  allocation: {method: rotation, rotation: {top: 3, factors: [{name: return, window: 5}]}}\n", "rotation.top"},
		"未知因子":              {poolHead + "  allocation: {method: rotation, rotation: {top: 1, factors: [{name: sharpe, window: 5}]}}\n", "不受支持"},
		"因子窗口为 0":           {poolHead + "  allocation: {method: rotation, rotation: {top: 1, factors: [{name: return, window: 0}]}}\n", "window"},
		"因子权重非正":            {poolHead + "  allocation: {method: rotation, rotation: {top: 1, factors: [{name: return, window: 5, weight: 0}]}}\n", "weight"},
		"因子方向非法":            {poolHead + "  allocation: {method: rotation, rotation: {top: 1, factors: [{name: return, window: 5, order: up}]}}\n", "order"},
		"择时窗口非法":            {poolHead + okAllocation + "  timing: {window: 0}\n", "timing.window"},
		"步长非正":              {poolHead + okAllocation + "  step: 0\n", "step"},
		"费率过大":              {poolHead + okAllocation + "  fees: {spot_bps: 5000}\n", "fees.spot_bps"},
		"未知字段":              {poolHead + okAllocation + "  extra: 1\n", "未知字段"},
		"leverage 超限":       {poolHead + okAllocation + "portfolio: {leverage: 11}\n", "leverage"},
	}
	for name, tc := range cases {
		_, err := Parse([]byte(tc.doc))
		if err == nil || !strings.Contains(err.Error(), tc.contains) {
			t.Fatalf("%s：期望包含 %q，实际 %v", name, tc.contains, err)
		}
	}
}

func TestValidatePoolMembers(t *testing.T) {
	t.Parallel()
	pool, err := parsePool(t, "  allocation: {method: fixed, ratios: {a: 1, b: 1}}\n")
	if err != nil {
		t.Fatal(err)
	}
	member := func(extra string) Strategy {
		strategy, err := Parse([]byte("name: m\n" + extra + "rules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\n"))
		if err != nil {
			t.Fatal(err)
		}
		return strategy
	}
	spot := member("market: {select: spot}\n")
	swap := member("market: {select: swap}\n")
	mode, err := ValidatePoolMembers(pool, map[string]Strategy{"a": spot, "b": swap})
	if err != nil || mode != (Market{Select: MarketBoth, Prefer: MarketSpot}) {
		t.Fatalf("现货加永续的并集：%+v，%v", mode, err)
	}
	if mode, err = ValidatePoolMembers(pool, map[string]Strategy{"a": spot, "b": spot}); err != nil || mode != SingleMarket(MarketSpot) {
		t.Fatalf("全是现货：%+v，%v", mode, err)
	}
	if mode, err = ValidatePoolMembers(pool, map[string]Strategy{"a": swap, "b": member("market: {select: swap}\nbar: 1h\n")}); err != nil || mode != SingleMarket(MarketSwap) {
		t.Fatalf("全是永续：%+v，%v", mode, err)
	}
	bad := map[string]struct {
		members  map[string]Strategy
		contains string
	}{
		"成员不存在":      {map[string]Strategy{"a": spot}, "不存在"},
		"成员 bar 不一致": {map[string]Strategy{"a": spot, "b": member("market: {select: spot}\nbar: 5m\n")}, "不一致"},
		"成员是池":       {map[string]Strategy{"a": spot, "b": pool}, "不能嵌套"},
		"成员用 factor_params": {map[string]Strategy{"a": spot, "b": func() Strategy {
			strategy := member("market: {select: spot}\n")
			strategy.FactorParams = map[string]map[string]ParamExpr{"m": {"p": {Default: "1"}}}
			return strategy
		}()}, "factor_params"},
	}
	// S8 取消了“成员必须写 market”：股票成员不写 market，保存时放行，启用时按池绑定的任务判断（见 TestInferPoolMemberMarkets）。
	if _, err := ValidatePoolMembers(pool, map[string]Strategy{"a": spot, "b": member("")}); err != nil {
		t.Fatalf("没写 market 的成员保存时放行：%v", err)
	}
	for name, tc := range bad {
		if _, err := ValidatePoolMembers(pool, tc.members); err == nil || !strings.Contains(err.Error(), tc.contains) {
			t.Fatalf("%s：期望包含 %q，实际 %v", name, tc.contains, err)
		}
	}
	leveraged, err := parsePool(t, "  allocation: {method: fixed, ratios: {a: 1, b: 1}}\nportfolio: {leverage: 2}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePoolMembers(leveraged, map[string]Strategy{"a": spot, "b": spot}); err == nil || !strings.Contains(err.Error(), "leverage") {
		t.Fatalf("成员只涉及现货时池不能加杠杆：%v", err)
	}
	if _, err := ValidatePoolMembers(leveraged, map[string]Strategy{"a": spot, "b": swap}); err != nil {
		t.Fatalf("涉及永续时可以加杠杆：%v", err)
	}
}

func TestMaxHoldBars(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(`name: h
rules:
  - id: r
    type: rank
    score: close
    select: {top: 1}
    weight: 1
    holding: {bars: 12, offsets: [0, 6]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strategy.MaxHoldBars(BarClock{PerDay: 24}); got != 12 {
		t.Fatalf("最长持有期 %d，期望 12", got)
	}
}
