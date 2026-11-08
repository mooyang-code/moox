package exchange

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

func TestStockFee(t *testing.T) {
	rate := shared.MustDecimal("0.00025")
	for _, tc := range []struct {
		side     Side
		notional string
		want     string
	}{
		{SideBuy, "1000", "5.01"},    // 佣金 0.25 按最低 5 元，过户费 0.01
		{SideSell, "1000", "5.51"},   // 再加印花税 0.5
		{SideBuy, "1000000", "260"},  // 佣金 250，过户费 10
		{SideSell, "1000000", "760"}, // 再加印花税 500
		{SideBuy, "0", "0"},          // 没有成交不收最低佣金
	} {
		if got := StockFee(tc.side, shared.MustDecimal(tc.notional), rate).String(); got != tc.want {
			t.Fatalf("StockFee(%s, %s) = %s，期望 %s", tc.side, tc.notional, got, tc.want)
		}
	}
	if got := StockBuyCost(shared.MustDecimal("1000"), rate).String(); got != "1005.01" {
		t.Fatalf("StockBuyCost = %s", got)
	}
}

func TestT1SinceIsBeijingMidnight(t *testing.T) {
	// 北京时间 11-06 00:30 是 UTC 11-05 16:30：当日从北京时间零点算。
	now := time.Date(2026, 11, 5, 16, 30, 0, 0, time.UTC)
	if got := T1Since(now); !got.Equal(time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("T1Since = %s", got)
	}
}

func TestStockRuleHasNoMinimumNotional(t *testing.T) {
	instrument := Instrument{MarketType: MarketTypeStock, MinExchangeQuantity: shared.MustDecimal("100")}
	if !instrument.MinNotionalAt(shared.MustDecimal("10")).IsZero() {
		t.Fatal("A 股没有最小名义金额，零股卖单不能被挡")
	}
}
