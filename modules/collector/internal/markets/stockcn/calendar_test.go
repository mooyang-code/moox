package stockcn

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCalendarHonorsHolidayMiddayAndHorizon(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stock_cn/calendar.yaml")
	require.NoError(t, err)
	require.NoError(t, calendar.ValidateHorizon(time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), 30))

	holiday := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	require.False(t, calendar.IsOpen(holiday))

	morning := time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC)
	require.True(t, calendar.IsOpen(morning))

	midday := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
	require.False(t, calendar.IsOpen(midday))
}

func TestCalendarTradingDaysAndExpectedBars(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stock_cn/calendar.yaml")
	require.NoError(t, err)

	location, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 3)

	days, err := calendar.TradingDays(start, end)
	require.NoError(t, err)
	require.Len(t, days, 1)
	require.Equal(t, "2026-01-02", days[0].TradeDate)
	require.Len(t, days[0].Sessions, 2)

	bars, err := calendar.ExpectedMinuteBars("2026-01-02")
	require.NoError(t, err)
	require.Len(t, bars, 240)

	require.Equal(t, "09:30", bars[0].In(location).Format("15:04"))
	require.Equal(t, "14:59", bars[len(bars)-1].In(location).Format("15:04"))
}
