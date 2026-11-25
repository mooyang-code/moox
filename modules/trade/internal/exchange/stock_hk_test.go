package exchange

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// 港股费用各项（佣金费率 0.03%）：印花税双边、向上取整到元、最低 1 港元；股份交收费 0.0042%、没有最低最高；佣金最低 3 港元。
func TestHKFee(t *testing.T) {
	rate := shared.MustDecimal("0.0003")
	for _, tc := range []struct {
		name     string
		notional string
		want     string
	}{
		// 佣金 11.28 + 印花税 ceil(37.6)=38 + 交易费 2.1244 + 证监会征费 1.0152 + 会财局征费 0.0564 + 交收费 1.5792
		{"一手腾讯", "37600", "54.0552"},
		// 佣金 0.15→3 + 印花税 0.5→1 + 0.02825 + 0.0135 + 0.00075 + 交收费 0.021
		{"佣金和印花税取最低", "500", "4.0635"},
		// 印花税正好 1 港元不再进位：3 + 1 + 0.0565 + 0.027 + 0.0015 + 2
		{"印花税正好整元", "1000", "4.127"},
		// 印花税 1.001 进到 2：3 + 2 + 0.0565565 + 0.027027 + 0.0015015 + 2
		{"印花税向上取整", "1001", "5.127127"},
		// 佣金 3000 + 印花税 10000 + 565 + 270 + 15 + 交收费 420（不封顶）
		{"大额成交交收费不封顶", "10000000", "14270"},
		{"没有成交不收最低费用", "0", "0"},
	} {
		for _, side := range []Side{SideBuy, SideSell} {
			if got := HKFee(side, shared.MustDecimal(tc.notional), rate).String(); got != tc.want {
				t.Fatalf("%s：HKFee(%s, %s) = %s，期望 %s", tc.name, side, tc.notional, got, tc.want)
			}
		}
	}
	rule := RuleAt(ExchangeHKEX, MarketTypeEquity)
	if got := rule.BuyCost(shared.MustDecimal("37600"), rate).String(); got != "37654.0552" {
		t.Fatalf("港股买入花费 = %s", got)
	}
}

// 港交所价位表：价格在档位边界上属于较低的一档。
func TestHKPriceTick(t *testing.T) {
	for price, want := range map[string]string{
		"0.137": "0.001", "0.25": "0.001", "0.251": "0.005", "0.5": "0.005", "1.005": "0.005", "9.99": "0.005", "10": "0.005",
		"10.01": "0.01", "18.81": "0.01", "20": "0.01", "20.02": "0.02", "29.08": "0.02", "50": "0.02", "68.85": "0.05", "100": "0.05",
		"138.5": "0.1", "200": "0.1", "376": "0.2", "500": "0.2", "635.5": "0.5", "1500": "1", "3000": "2", "5000": "2", "6000": "5",
	} {
		if got := HKPriceTick(shared.MustDecimal(price)).String(); got != want {
			t.Fatalf("HKPriceTick(%s) = %s，期望 %s", price, got, want)
		}
	}
}

// 限价对齐到价位表：买入向上、卖出向下，正好在价位上不动；没有价位表的场所（A 股、加密货币）原样返回。
func TestSnapPriceFollowsExchangeTickTable(t *testing.T) {
	hk := RuleAt(ExchangeHKEX, MarketTypeEquity)
	for _, tc := range []struct {
		price string
		side  Side
		want  string
	}{
		{"376.07", SideBuy, "376.2"}, {"376.07", SideSell, "376"}, {"376.2", SideBuy, "376.2"},
		{"9.9975", SideBuy, "10"}, {"10.004", SideSell, "10"}, {"10.004", SideBuy, "10.01"},
		{"29.071", SideBuy, "29.08"}, {"29.071", SideSell, "29.06"}, {"0.1374", SideSell, "0.137"},
	} {
		if got := hk.SnapPrice(shared.MustDecimal(tc.price), tc.side).String(); got != tc.want {
			t.Fatalf("港股 SnapPrice(%s, %s) = %s，期望 %s", tc.price, tc.side, got, tc.want)
		}
	}
	for _, rule := range []ExchangeRule{RuleAt(ExchangeAShare, MarketTypeEquity), RuleAt(ExchangeBinance, MarketTypeSpot)} {
		if got := rule.SnapPrice(shared.MustDecimal("376.07"), SideBuy).String(); got != "376.07" {
			t.Fatalf("没有价位表的场所不动限价：%s", got)
		}
	}
}

// 规则按“场所 + 市场”取：同是股票，A 股 T+1、有价格笼子、A 股费用；港股 T+0、没有价格笼子、港股费用、按价位分档。
// 整手、不能卖空、停牌顺延是股票共有的。
func TestRuleAtSeparatesStockExchanges(t *testing.T) {
	ashare, hk := RuleAt(ExchangeAShare, MarketTypeEquity), RuleAt("hkex", "equity")
	for name, rule := range map[string]ExchangeRule{"A 股": ashare, "港股": hk} {
		if !rule.BoardLot || !rule.QuoteGuards || !rule.PositionFromBalance || !rule.RawInstrumentID || rule.CanShort || rule.Leverage || rule.Fee == nil {
			t.Fatalf("%s 的股票共有规则不对：%+v", name, rule.MarketRule)
		}
	}
	if !ashare.T1 || ashare.PriceCageTicks != 10 || ashare.PriceTick != nil {
		t.Fatalf("A 股：T+1、价格笼子 10 个价位、固定价格步长：T1=%v cage=%d", ashare.T1, ashare.PriceCageTicks)
	}
	if hk.T1 || hk.PriceCageTicks != 0 || hk.PriceTick == nil {
		t.Fatalf("港股：T+0、没有价格笼子、按价位分档：T1=%v cage=%d", hk.T1, hk.PriceCageTicks)
	}
	rate, notional := shared.MustDecimal("0.0003"), shared.MustDecimal("1000")
	if a, h := ashare.Fee(SideBuy, notional, rate).String(), hk.Fee(SideBuy, notional, rate).String(); a != "5.01" || h != "4.127" {
		t.Fatalf("两个场所的费用函数不同：A 股 %s、港股 %s", a, h)
	}
	// 没有单独登记的场所只有市场类型共有的规则；未知市场是零值。
	if spot := RuleAt(ExchangeBinance, MarketTypeSpot); spot.T1 || spot.Fee != nil || !spot.PositionFromBalance {
		t.Fatalf("币安现货：%+v", spot.MarketRule)
	}
	if unknown := RuleAt("X", "UNKNOWN"); unknown.MarketRule != (MarketRule{}) || unknown.T1 || unknown.Fee != nil {
		t.Fatal("未知市场应返回零值")
	}
	if got := MarketsWhere(ExchangeAShare, func(rule ExchangeRule) bool { return rule.T1 }); len(got) != 1 || got[0] != MarketTypeEquity {
		t.Fatalf("A 股有 T+1 的市场是 STOCK：%v", got)
	}
	if got := MarketsWhere(ExchangeHKEX, func(rule ExchangeRule) bool { return rule.T1 }); len(got) != 0 {
		t.Fatalf("港股没有 T+1 的市场：%v", got)
	}
}
