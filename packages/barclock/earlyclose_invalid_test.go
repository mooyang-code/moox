package barclock

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
	"github.com/stretchr/testify/require"
)

// earlyAt 在 plainDays 之上加一个提前收市日，收市时刻由测试指定。
type earlyAt struct {
	plainDays
	date    marketcalendar.CivilDate
	closeAt time.Duration
}

func (d earlyAt) EarlyClose(date marketcalendar.CivilDate) (time.Duration, bool) {
	return d.closeAt, date == d.date
}

func (d earlyAt) EarlyCloseDates() []marketcalendar.CivilDate {
	return []marketcalendar.CivilDate{d.date}
}

// 提前收市日的收市时刻不晚于开盘、或开盘后不足一根，是坏的日历数据：构造时钟和交易时段判断时直接拒绝。
func TestEarlyCloseBeforeOpenIsRejected(t *testing.T) {
	calendar, err := marketcalendar.Load("hk_stock")
	require.NoError(t, err)
	location, err := time.LoadLocation("Asia/Hong_Kong")
	require.NoError(t, err)
	days := func(closeAt time.Duration) earlyAt {
		return earlyAt{plainDays: plainDays{calendar}, date: mustCivil(t, "2026-12-24"), closeAt: closeAt}
	}
	open := 9*time.Hour + 30*time.Minute

	for _, closeAt := range []time.Duration{9 * time.Hour, open, open + 3*time.Minute} {
		_, err := NewSession(days(closeAt), location, hkSessions, 5*time.Minute)
		require.ErrorIs(t, err, ErrNoCalendar, "%s", closeAt)
	}
	for _, closeAt := range []time.Duration{9 * time.Hour, open} {
		_, err := NewSession(days(closeAt), location, hkSessions, 24*time.Hour)
		require.ErrorIs(t, err, ErrNoCalendar, "%s", closeAt)
		_, err = NewHours(days(closeAt), location, hkSessions)
		require.ErrorIs(t, err, ErrNoCalendar, "%s", closeAt)
	}

	// 开盘后正好一根就收市是合法的：当天只有 09:30 这一根。
	clock, err := NewSession(days(open+5*time.Minute), location, hkSessions, 5*time.Minute)
	require.NoError(t, err)
	buckets, err := clock.Between(hkt("2026-12-24 00:00"), hkt("2026-12-25 00:00"), 1000)
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	first, err := clock.Ordinal(buckets[0])
	require.NoError(t, err)
	next, err := clock.BucketAtOrdinal(first + 1)
	require.NoError(t, err)
	require.Equal(t, hkt("2026-12-28 09:30"), next.Start)
}
