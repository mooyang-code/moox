package marketwiring

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

func TestStockCNPlanPeriodValidFollowsTheTradingCalendar(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	t.Setenv("MOOX_STOCK_CN_CALENDAR_PATH", filepath.Join(filepath.Dir(file), "..", "..", "config", "markets", "stockcn", "calendar.yaml"))
	valid := stockCNPlanPeriodValid()
	stock := marketfetch.PlanMarket{MarketID: marketfetch.StockCNSpaceID}
	shanghai := time.FixedZone("CST", 8*3600)
	// 2026-10-12（周一）：开盘第一分钟、午休、收盘前最后一分钟、收盘后。
	require.True(t, valid(stock, "1m", time.Date(2026, 10, 12, 9, 30, 0, 0, shanghai).UTC()))
	require.False(t, valid(stock, "1m", time.Date(2026, 10, 12, 12, 0, 0, 0, shanghai).UTC()), "lunch break")
	require.True(t, valid(stock, "1m", time.Date(2026, 10, 12, 14, 59, 0, 0, shanghai).UTC()))
	require.False(t, valid(stock, "1m", time.Date(2026, 10, 12, 15, 30, 0, 0, shanghai).UTC()), "after close")
	require.False(t, valid(stock, "1m", time.Date(2026, 10, 11, 10, 0, 0, 0, shanghai).UTC()), "weekend")
	require.False(t, valid(stock, "5m", time.Date(2026, 10, 12, 10, 0, 0, 0, shanghai).UTC()), "only 1m is supported")
	require.True(t, valid(marketfetch.PlanMarket{MarketID: "crypto"}, "1m", time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)), "crypto is continuous")
}
