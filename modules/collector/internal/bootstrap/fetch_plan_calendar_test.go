package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

const testCalendarYAML = `timezone: Asia/Shanghai
coverage_start: "2026-01-01"
coverage_end: "2026-12-31"
sessions:
    - start: "09:30"
      end: "11:30"
closed_dates: []
exceptional_open_dates: []
`

func TestStockCNCheckCalendarReportsErrorsInsteadOfNonTrading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calendar.yaml")
	t.Setenv("MOOX_STOCK_CN_CALENDAR_PATH", path)
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	filter := newStockCNCheckCalendar()
	filter.now = func() time.Time { return now }

	// 文件缺失：不可用，且失败不缓存。
	require.Error(t, filter.ready(marketfetch.StockCNSpaceID))
	require.NoError(t, os.WriteFile(path, []byte(testCalendarYAML), 0o600))
	require.NoError(t, filter.ready(marketfetch.StockCNSpaceID))

	// 交易分钟有效，休市分钟无效，均无错误。
	open := time.Date(2026, 10, 12, 1, 30, 0, 0, time.UTC) // 09:30 CST（周一）
	ok, err := filter.valid(marketfetch.StockCNSpaceID, "1m", open)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = filter.valid(marketfetch.StockCNSpaceID, "1m", open.Add(3*time.Hour))
	require.NoError(t, err)
	require.False(t, ok)

	// 超出覆盖期与不支持的频率：报错，不能当作非交易时间。
	_, err = filter.valid(marketfetch.StockCNSpaceID, "1m", time.Date(2027, 1, 5, 2, 0, 0, 0, time.UTC))
	require.Error(t, err)
	_, err = filter.valid(marketfetch.StockCNSpaceID, "1h", open)
	require.Error(t, err)

	// 日历文件更新后，超过重载间隔即生效；重载失败沿用旧日历。
	require.NoError(t, os.WriteFile(path, []byte("坏文件"), 0o600))
	now = now.Add(2 * time.Hour)
	require.NoError(t, filter.ready(marketfetch.StockCNSpaceID))
	ok, err = filter.valid(marketfetch.StockCNSpaceID, "1m", open)
	require.NoError(t, err)
	require.True(t, ok)
}
