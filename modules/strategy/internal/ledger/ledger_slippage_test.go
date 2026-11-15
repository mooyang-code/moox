package ledger

import (
	"encoding/json"
	"strings"
	"testing"
)

// 滑点 100 bps、不计手续费：现货全仓买入等价于按 10.1 成交，卖出等价于按 9.9 成交；持仓仍按收盘价估值。
func TestLedgerSlippageMovesSpotFillPrice(t *testing.T) {
	ledger := NewLedger(100, 0, 0, 3)
	ledger.SlippageRate = 0.01
	buy := ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 10}, Targets: map[string]float64{"A": 1}})
	// 按 10.1 买入 100 元：数量 100 / 10.1，按收盘价 10 估值 100 / 1.01。
	near(t, "买入数量", ledger.Positions["A"].Quantity, 100/10.1)
	near(t, "持仓按收盘价估值", ledger.Positions["A"].Value, 100/1.01)
	near(t, "持仓价格是收盘价", ledger.Positions["A"].LastPrice, 10)
	near(t, "现金用完", ledger.Cash, 0)
	near(t, "买入滑点成本", buy.Slippage, 1/1.01)
	near(t, "手续费为 0", buy.Fee, 0)
	near(t, "成交后权益", buy.EquityAfter, 100/1.01)
	if buy.BuyScale != nil {
		t.Fatalf("迭代已扣掉滑点，买入不应被缩减：%v", *buy.BuyScale)
	}
	// 同价全部卖出：按 9.9 成交。
	sell := ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 10}, Targets: map[string]float64{}})
	near(t, "卖出后现金", ledger.Cash, 100/10.1*9.9)
	near(t, "卖出滑点成本", sell.Slippage, 1/1.01)
	if len(ledger.Positions) != 0 {
		t.Fatalf("应已清仓：%v", ledger.Positions)
	}
}

// 永续开空：滑点与手续费都从现金扣，数量按收盘价的名义金额算，下一根按收盘价差结算。
func TestLedgerSlippageOnSwapAndFee(t *testing.T) {
	ledger := NewLedger(100, 0, 5, 3)
	ledger.SlippageRate = 0.001
	open := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 10}, Targets: map[string]float64{swapA: -1}})
	// 目标名义价值 = 扣掉费用后的权益：x = 100 − (0.0005 + 0.001) x。
	notional := 100 / 1.0015
	near(t, "空头数量", ledger.Positions[swapA].Quantity, -notional/10)
	near(t, "持仓价格是收盘价", ledger.Positions[swapA].LastPrice, 10)
	near(t, "手续费", open.Fee, notional*0.0005)
	near(t, "滑点成本", open.Slippage, notional*0.001)
	near(t, "现金扣掉手续费与滑点", ledger.Cash, notional)
	// 价格跌到 9：空头盈利按收盘价差 1 × 数量结算。
	next := ledger.Step(Step{OK: true, Prices: map[string]float64{swapA: 9}, Targets: map[string]float64{swapA: -1}})
	near(t, "成交前权益", next.EquityBefore, notional+notional/10)
}

// 滑点为 0 时输出里不出现滑点字段（回放的逐期摘要与金样逐字节不变）。
func TestLedgerZeroSlippageIsOmitted(t *testing.T) {
	ledger := NewLedger(100, 10, 5, 3)
	outcome := ledger.Step(Step{OK: true, Prices: map[string]float64{"A": 10}, Targets: map[string]float64{"A": 1}})
	encoded, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	book, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "slippage") || strings.Contains(string(book), "slippage") {
		t.Fatalf("滑点为 0 时不应输出：%s %s", encoded, book)
	}
}
