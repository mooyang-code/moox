package marketwiring

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

func TestPlanPeriodValidFollowsTheMarketClock(t *testing.T) {
	stock := marketfetch.PlanMarket{MarketID: marketfetch.StockCNSpaceID}
	shanghai := time.FixedZone("CST", 8*3600)
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, 10, day, hour, minute, 0, 0, shanghai).UTC()
	}
	// 2026-10-12（周一）。
	require.True(t, planPeriodValid(stock, "1m", at(12, 9, 30)))
	require.False(t, planPeriodValid(stock, "1m", at(12, 12, 0)), "午休")
	require.True(t, planPeriodValid(stock, "1m", at(12, 14, 59)))
	require.False(t, planPeriodValid(stock, "1m", at(12, 15, 30)), "收盘后")
	require.False(t, planPeriodValid(stock, "1m", at(11, 10, 0)), "周末")
	require.True(t, planPeriodValid(stock, "5m", at(12, 11, 25)), "11:30 触发采 11:25 那根")
	require.False(t, planPeriodValid(stock, "5m", at(12, 12, 55)), "13:00 触发对应的 12:55 不是交易桶")
	require.True(t, planPeriodValid(stock, "5m", at(12, 14, 55)), "15:00 触发采 14:55 那根")
	require.False(t, planPeriodValid(stock, "5m", at(11, 10, 0)), "非交易日")
	require.False(t, planPeriodValid(stock, "5m", time.Date(2027, 1, 5, 2, 0, 0, 0, time.UTC)), "超出日历覆盖期不执行")
	require.True(t, planPeriodValid(marketfetch.PlanMarket{MarketID: "crypto"}, "5m", at(11, 3, 0)), "加密货币连续交易")
}
