package barclock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func stockHours(t *testing.T) *Hours {
	t.Helper()
	market, err := MarketFor("stockcn")
	require.NoError(t, err)
	hours, err := market.Hours()
	require.NoError(t, err)
	return hours
}

// cst 把北京时间写成 UTC 时刻。
func cst(value string) time.Time {
	parsed, err := time.Parse("2006-01-02T15:04:05", value)
	if err != nil {
		panic(err)
	}
	return parsed.Add(-8 * time.Hour).UTC()
}

func TestHoursInSessionCoversLunchBreakAndClose(t *testing.T) {
	hours := stockHours(t)
	cases := map[string]bool{
		"2026-11-06T09:29:59": false,
		"2026-11-06T09:30:00": true,
		"2026-11-06T11:29:59": true,
		"2026-11-06T11:30:00": false, // 午休
		"2026-11-06T12:59:59": false,
		"2026-11-06T13:00:00": true,
		"2026-11-06T14:59:59": true,
		"2026-11-06T15:00:00": false, // 收盘
		"2026-11-07T10:00:00": false, // 周六
		"2026-10-01T10:00:00": false, // 国庆
	}
	for at, want := range cases {
		got, err := hours.InSession(cst(at))
		require.NoError(t, err, at)
		require.Equal(t, want, got, at)
	}
}

func TestHoursNextOpenCrossesLunchWeekendAndHoliday(t *testing.T) {
	hours := stockHours(t)
	cases := map[string]string{
		"2026-11-06T11:30:00": "2026-11-06T13:00:00", // 午休 → 下午开盘
		"2026-11-06T15:00:00": "2026-11-09T09:30:00", // 周五收盘 → 周一
		"2026-09-30T15:00:00": "2026-10-08T09:30:00", // 长假前最后一个交易日 → 节后第一天
		"2026-11-09T08:00:00": "2026-11-09T09:30:00",
	}
	for at, want := range cases {
		got, err := hours.NextOpen(cst(at))
		require.NoError(t, err, at)
		require.Equal(t, cst(want), got, at)
	}
}

func TestHoursLastBreakAndLastClose(t *testing.T) {
	hours := stockHours(t)
	breakCases := map[string]string{
		"2026-11-06T12:00:00": "2026-11-06T11:30:00",
		"2026-11-06T11:30:00": "2026-11-06T11:30:00",
		"2026-11-06T14:00:00": "2026-11-06T11:30:00",
		"2026-11-07T20:00:00": "2026-11-06T15:00:00",
		"2026-11-09T09:00:00": "2026-11-06T15:00:00",
		"2026-10-05T10:00:00": "2026-09-30T15:00:00",
	}
	for at, want := range breakCases {
		got, err := hours.LastBreak(cst(at))
		require.NoError(t, err, at)
		require.Equal(t, cst(want), got, at)
	}
	closeCases := map[string]string{
		// 午间只看日收盘：仍是上一个交易日的 15:00，不会把上午的挂单当成收盘前的。
		"2026-11-09T12:00:00": "2026-11-06T15:00:00",
		"2026-11-09T15:00:00": "2026-11-09T15:00:00",
		"2026-11-09T20:00:00": "2026-11-09T15:00:00",
	}
	for at, want := range closeCases {
		got, err := hours.LastClose(cst(at))
		require.NoError(t, err, at)
		require.Equal(t, cst(want), got, at)
	}
}

func TestHoursOutOfCoverageReportsError(t *testing.T) {
	hours := stockHours(t)
	_, err := hours.InSession(cst("2028-01-04T10:00:00"))
	require.ErrorIs(t, err, ErrOutOfCoverage)
	_, err = hours.LastClose(cst("2028-01-04T10:00:00"))
	require.ErrorIs(t, err, ErrOutOfCoverage)
	_, err = hours.NextOpen(cst("2027-12-31T15:00:00"))
	require.ErrorIs(t, err, ErrOutOfCoverage)
}

func TestEpochMarketHasNoHours(t *testing.T) {
	market, err := MarketFor("crypto")
	require.NoError(t, err)
	_, err = market.Hours()
	require.ErrorIs(t, err, ErrNoCalendar)
}
