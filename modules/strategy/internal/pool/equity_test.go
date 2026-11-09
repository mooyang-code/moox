package pool

import (
	"math"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
)

// S8：股票成员停牌（连续缺价）时按最后价格保持持仓、不清算，费率用 fees.equity_bps；同样的输入在现货池里缺价 3 期后清算。
func TestEquityMemberKeepsSuspendedHolding(t *testing.T) {
	strategy, err := dsl.Parse([]byte("name: pool\nbar: 1d\nstrategy_pool:\n  members:\n    - {id: a, strategy: s_a}\n  allocation: {method: fixed, ratios: {a: 1}}\n  fees: {spot_bps: 0, equity_bps: 10}\n"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(mode dsl.Market, key string) (engine.State, float64) {
		cfg, err := NewConfig(strategy, mode, nil, engine.PoolClock{})
		if err != nil {
			t.Fatal(err)
		}
		state := engine.State{}
		var equity float64
		for bar := int64(1); bar <= 6; bar++ {
			prices := map[string]float64{}
			if bar == 1 {
				prices[key] = 10
			}
			decision, err := Step(cfg, state, Input{BarIndex: bar, Prices: prices, Members: []MemberResult{pass("a", map[string]string{key: "1"})}})
			if err != nil {
				t.Fatal(err)
			}
			state, equity = decision.State, decision.Summary.Pool.Members[0].Equity
		}
		return state, equity
	}
	stockState, stockEquity := run(dsl.SingleMarket(dsl.MarketEquity), "A|equity")
	decoded, err := DecodeState(stockState.Pool)
	if err != nil {
		t.Fatal(err)
	}
	book := decoded.Ledgers["a"]
	position, held := book.Positions["A|equity"]
	if !held || position.MissingBars != 5 || book.LiquidateAfter != 0 {
		t.Fatalf("停牌的股票持仓应保持：%+v", book)
	}
	// 买入 1 × (1 − fee) 的市值，手续费 10 bps：权益 = 1 − 买入额 × 0.001，买入额 = 1 / 1.001。
	if want := 1 - 0.001/1.001; math.Abs(stockEquity-want) > 1e-9 {
		t.Fatalf("股票费率按 equity_bps：权益 %v，期望 %v", stockEquity, want)
	}
	spotState, _ := run(dsl.SingleMarket(dsl.MarketSpot), "A|spot")
	decoded, _ = DecodeState(spotState.Pool)
	if _, held := decoded.Ledgers["a"].Positions["A|spot"]; held {
		t.Fatal("现货池缺价 3 期后应清算")
	}
}
