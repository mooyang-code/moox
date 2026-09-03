package stockcn

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCalendarHonorsHolidayMiddayAndHorizon(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stockcn/calendar.yaml")
	require.NoError(t, err)
	require.NoError(t, calendar.ValidateHorizon(time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), 30))

	holiday := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	require.False(t, calendar.IsOpen(holiday))

	morning := time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC)
	require.True(t, calendar.IsOpen(morning))

	midday := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
	require.False(t, calendar.IsOpen(midday))
}

func TestCalendarContainsEveryOfficial2026WeekdayClosure(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stockcn/calendar.yaml")
	require.NoError(t, err)
	location := calendar.Location()
	for _, date := range []string{
		"2026-01-01", "2026-01-02",
		"2026-02-16", "2026-02-17", "2026-02-18", "2026-02-19", "2026-02-20", "2026-02-23",
		"2026-04-06",
		"2026-05-01", "2026-05-04", "2026-05-05",
		"2026-06-19",
		"2026-09-25",
		"2026-10-01", "2026-10-02", "2026-10-05", "2026-10-06", "2026-10-07",
	} {
		day, parseErr := time.ParseInLocation("2006-01-02 15:04", date+" 10:00", location)
		require.NoError(t, parseErr)
		require.Falsef(t, calendar.IsOpen(day), "%s must be closed", date)
	}
}

func TestCalendarHorizonDistinguishesWarningFromExpired(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stockcn/calendar.yaml")
	require.NoError(t, err)
	location := calendar.Location()

	err = calendar.CheckHorizon(time.Date(2026, 12, 20, 10, 0, 0, 0, location), 14)
	require.ErrorIs(t, err, ErrCalendarExpiringSoon)
	require.False(t, errors.Is(err, ErrCalendarExpired))
	require.NoError(t, calendar.ValidateHorizon(time.Date(2026, 12, 20, 10, 0, 0, 0, location), 14))

	err = calendar.CheckHorizon(time.Date(2026, 12, 31, 14, 59, 0, 0, location), 14)
	require.ErrorIs(t, err, ErrCalendarExpiringSoon)

	err = calendar.CheckHorizon(time.Date(2027, 1, 1, 0, 0, 0, 0, location), 14)
	require.ErrorIs(t, err, ErrCalendarExpired)
	require.ErrorIs(t, calendar.ValidateHorizon(time.Date(2027, 1, 1, 0, 0, 0, 0, location), 14), ErrCalendarExpired)
}

func TestCalendarHorizonWarningIsLimitedToOncePerLocalDay(t *testing.T) {
	coverage := t.Name()
	require.True(t, shouldLogHorizonWarning(coverage, "2026-12-20"))
	require.False(t, shouldLogHorizonWarning(coverage, "2026-12-20"))
	require.True(t, shouldLogHorizonWarning(coverage, "2026-12-21"))
}

func TestCalendarFailsClosedOutsideConfiguredCoverage(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stockcn/calendar.yaml")
	require.NoError(t, err)
	location := calendar.Location()
	require.False(t, calendar.IsOpen(time.Date(2025, 1, 1, 10, 0, 0, 0, location)))
	require.False(t, calendar.IsOpen(time.Date(2027, 1, 4, 10, 0, 0, 0, location)))
}

func TestCalendarTradingDaysAndExpectedBars(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stockcn/calendar.yaml")
	require.NoError(t, err)

	location, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 1)

	days, err := calendar.TradingDays(start, end)
	require.NoError(t, err)
	require.Len(t, days, 1)
	require.Equal(t, "2026-01-05", days[0].TradeDate)
	require.Len(t, days[0].Sessions, 2)

	bars, err := calendar.ExpectedMinuteBars("2026-01-05")
	require.NoError(t, err)
	require.Len(t, bars, 240)

	require.Equal(t, "09:30", bars[0].In(location).Format("15:04"))
	require.Equal(t, "14:59", bars[len(bars)-1].In(location).Format("15:04"))
}

func TestCalendarLookbackCountsTradingDays(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stockcn/calendar.yaml")
	require.NoError(t, err)

	start, err := calendar.LookbackStart(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), 2)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 30, 1, 30, 0, 0, time.UTC), start)
}

func TestCalendarLatestClosedMinuteWalksBackAcrossClosedDays(t *testing.T) {
	calendar, err := LoadCalendar("../../../config/markets/stockcn/calendar.yaml")
	require.NoError(t, err)

	start, end, err := calendar.LatestClosedMinute(time.Date(2026, 8, 30, 4, 0, 0, 0, time.UTC), 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 8, 28, 6, 59, 0, 0, time.UTC), start)
	require.Equal(t, time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC), end)

	start, end, err = calendar.LatestClosedMinute(time.Date(2026, 10, 3, 4, 0, 0, 0, time.UTC), 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 30, 6, 59, 0, 0, time.UTC), start)
	require.Equal(t, time.Date(2026, 9, 30, 7, 0, 0, 0, time.UTC), end)
}
