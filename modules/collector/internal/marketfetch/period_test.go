package marketfetch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPeriodWindowsUsesStorageCalendar(t *testing.T) {
	now := time.Date(2026, time.August, 9, 12, 3, 45, 0, time.UTC)
	windows, err := periodWindows(now, "1m")
	require.NoError(t, err)
	require.Len(t, windows, 2)
	require.Equal(t, time.Date(2026, time.August, 9, 12, 3, 0, 0, time.UTC), windows[0].PeriodTime)
	require.Equal(t, time.Date(2026, time.August, 9, 12, 4, 0, 0, time.UTC), windows[0].CloseAt)
	require.Equal(t, time.Date(2026, time.August, 9, 12, 4, 0, 0, time.UTC), windows[1].PeriodTime)
	require.Equal(t, time.Date(2026, time.August, 9, 12, 5, 0, 0, time.UTC), windows[1].CloseAt)
}

func TestPeriodWindowsHandlesMonthBoundary(t *testing.T) {
	now := time.Date(2026, time.March, 1, 0, 1, 0, 0, time.UTC)
	windows, err := periodWindows(now, "1M")
	require.NoError(t, err)
	require.Len(t, windows, 2)
	require.Equal(t, time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC), windows[0].PeriodTime)
	require.Equal(t, time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC), windows[0].CloseAt)
	require.Equal(t, time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC), windows[1].PeriodTime)
	require.Equal(t, time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC), windows[1].CloseAt)
}

func TestReadinessGraceScalesWithFrequency(t *testing.T) {
	require.Equal(t, 2*time.Minute, readinessGrace("1m", 2*time.Minute))
	require.Equal(t, 10*time.Minute, readinessGrace("5m", 2*time.Minute))
	require.Equal(t, 10*time.Minute, readinessGrace("1h", 2*time.Minute))
	require.Equal(t, time.Second, readinessGrace("1h", time.Second))
}
