package paper

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"time"
)

func MarketExecutionPrice(side exchange.Side, quote execution.MarketQuote, slippageBPS shared.Decimal) (shared.Decimal, error) {
	if side != exchange.SideBuy && side != exchange.SideSell {
		return shared.Decimal{}, fmt.Errorf("paper: order side is required")
	}
	if slippageBPS.IsNegative() || slippageBPS.Cmp(shared.MustDecimal("10000")) >= 0 {
		return shared.Decimal{}, fmt.Errorf("paper: invalid slippage")
	}
	base := quote.Ask
	if side == exchange.SideSell {
		base = quote.Bid
	}
	if base.IsZero() {
		base = quote.Last
	}
	if base.IsZero() || quote.SourceTime.IsZero() {
		return shared.Decimal{}, fmt.Errorf("paper: quote is unavailable")
	}
	factor := shared.MustDecimal("1").Add(slippageBPS.Div(shared.MustDecimal("10000")))
	if side == exchange.SideSell {
		factor = shared.MustDecimal("1").Sub(slippageBPS.Div(shared.MustDecimal("10000")))
	}
	return base.Mul(factor), nil
}

func LimitMarketable(side exchange.Side, limit, last shared.Decimal) bool {
	if side == exchange.SideBuy {
		return last.Cmp(limit) <= 0
	}
	return last.Cmp(limit) >= 0
}

// strictQuoteFresh 是撮合和估值用的报价新鲜度：没有交易时段的场所（加密货币）保持原来的判断——报价时间不能晚于现在、
// 不超过 maxAge；有交易时段的场所（A 股）按 execution.QuoteFresh，时段外用收盘价。
func strictQuoteFresh(exchanges execution.ExchangeCatalog, name string, at, now time.Time, maxAge time.Duration) bool {
	if execution.HasSession(exchanges, name) {
		return execution.QuoteFresh(exchanges, name, at, now, maxAge)
	}
	return !at.IsZero() && !at.After(now) && now.Sub(at) <= maxAge
}
