package barclock

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func utc(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

func TestEpochClockBuckets(t *testing.T) {
	clock, err := NewEpoch(45 * time.Minute)
	require.NoError(t, err)

	bucket, ok, err := clock.BucketAt(utc("2026-10-21T10:20:00Z"))
	require.NoError(t, err)
	require.True(t, ok)
	// 45m 一天正好 32 根：10:20 落在 09:45–10:30。
	require.Equal(t, utc("2026-10-21T09:45:00Z"), bucket.Start)
	require.Equal(t, utc("2026-10-21T10:30:00Z"), bucket.End)
	require.Equal(t, bucket.Start, bucket.Key)

	latest, err := clock.LatestClosed(utc("2026-10-21T10:30:00Z"))
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-21T09:45:00Z"), latest.Start, "收盘时刻等于 now 的桶已收盘")
	latest, err = clock.LatestClosed(utc("2026-10-21T10:29:59Z"))
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-21T09:00:00Z"), latest.Start)

	// 跨天：每天 00:00 UTC 重新起桶，最后一根是 23:15–24:00。
	last, err := clock.LatestClosed(utc("2026-10-22T00:00:00Z"))
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-21T23:15:00Z"), last.Start)
	next, err := clock.Next(last)
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-22T00:00:00Z"), next.Start)

	recent, err := clock.ClosedBefore(utc("2026-10-21T10:30:00Z"), 3)
	require.NoError(t, err)
	require.Len(t, recent, 3)
	require.Equal(t, utc("2026-10-21T08:15:00Z"), recent[0].Start)
	require.Equal(t, utc("2026-10-21T09:45:00Z"), recent[2].Start)
}

func TestEpochClockSourceTimes(t *testing.T) {
	clock, err := NewEpoch(45 * time.Minute)
	require.NoError(t, err)
	bucket, _, _ := clock.BucketAt(utc("2026-10-21T09:50:00Z"))
	times, err := clock.SourceTimes(bucket, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, times, 9)
	require.Equal(t, utc("2026-10-21T09:45:00Z"), times[0])
	require.Equal(t, utc("2026-10-21T10:25:00Z"), times[8])

	require.ErrorIs(t, clock.ValidateSource(30*time.Minute), ErrPeriodNotDivisible, "30m 不能整除 45m")
	require.ErrorIs(t, clock.ValidateSource(45*time.Minute+time.Minute), ErrPeriodNotDivisible)
	require.NoError(t, clock.ValidateSource(15*time.Minute))
}

func TestEpochClockBetween(t *testing.T) {
	clock, _ := NewEpoch(time.Hour)
	buckets, err := clock.Between(utc("2026-10-21T10:30:00Z"), utc("2026-10-21T13:00:00Z"), 100)
	require.NoError(t, err)
	require.Len(t, buckets, 3, "10:00、11:00、12:00 三个桶与区间有交集")
	require.Equal(t, utc("2026-10-21T10:00:00Z"), buckets[0].Start)
	limited, _ := clock.Between(utc("2026-10-21T00:00:00Z"), utc("2026-10-22T00:00:00Z"), 5)
	require.Len(t, limited, 5)
}

func TestNewEpochRejectsNonDividingPeriods(t *testing.T) {
	for _, period := range []time.Duration{0, 30 * time.Second, 7 * time.Minute, 5 * time.Hour, 48 * time.Hour} {
		_, err := NewEpoch(period)
		require.Error(t, err, period.String())
	}
}

func stockClock(t *testing.T, period time.Duration) Clock {
	t.Helper()
	clock, err := ClockFor("stockcn", period)
	require.NoError(t, err)
	return clock
}

func TestSessionClockHourlyBucketsMatchTradingSessions(t *testing.T) {
	clock := stockClock(t, time.Hour)
	// 2026-10-21 是周三。北京时间 09:30 = 01:30Z。
	buckets, err := clock.Between(utc("2026-10-21T00:00:00Z"), utc("2026-10-22T00:00:00Z"), 100)
	require.NoError(t, err)
	var starts []time.Time
	for _, bucket := range buckets {
		starts = append(starts, bucket.Start)
		require.Equal(t, bucket.Start, bucket.Key)
		require.Equal(t, time.Hour, bucket.End.Sub(bucket.Start))
	}
	require.Equal(t, []time.Time{
		utc("2026-10-21T01:30:00Z"), utc("2026-10-21T02:30:00Z"), // 09:30、10:30
		utc("2026-10-21T05:00:00Z"), utc("2026-10-21T06:00:00Z"), // 13:00、14:00
	}, starts)
}

func TestSessionClockBucketAtSkipsLunchAndNonTradingDays(t *testing.T) {
	clock := stockClock(t, time.Hour)
	_, ok, err := clock.BucketAt(utc("2026-10-21T04:00:00Z")) // 12:00 午休
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = clock.BucketAt(utc("2026-10-24T02:00:00Z")) // 周六
	require.NoError(t, err)
	require.False(t, ok)
	bucket, ok, err := clock.BucketAt(utc("2026-10-21T02:45:00Z")) // 10:45
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, utc("2026-10-21T02:30:00Z"), bucket.Start)
}

func TestSessionClockLatestClosedAndNavigationSkipBreaksAndHolidays(t *testing.T) {
	clock := stockClock(t, time.Hour)
	// 午休中：最新收盘的是上午最后一桶（10:30–11:30，即 03:30Z）。
	latest, err := clock.LatestClosed(utc("2026-10-21T04:00:00Z"))
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-21T02:30:00Z"), latest.Start)
	// 下一桶是下午的 13:00（05:00Z）。
	next, err := clock.Next(latest)
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-21T05:00:00Z"), next.Start)
	// 周一早盘开盘前：最新收盘的是上周五 14:00–15:00。
	latest, err = clock.LatestClosed(utc("2026-10-26T01:00:00Z"))
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-23T06:00:00Z"), latest.Start)
	next, err = clock.Next(latest)
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-26T01:30:00Z"), next.Start, "周末没有桶")
	prev, err := clock.Prev(next)
	require.NoError(t, err)
	require.Equal(t, latest.Start, prev.Start)

	// 国庆长假（2026-10-01 起休市）后的第一个桶。
	recent, err := clock.ClosedBefore(utc("2026-10-09T03:00:00Z"), 8)
	require.NoError(t, err)
	require.Len(t, recent, 8)
	for _, bucket := range recent {
		require.Equal(t, time.Hour, bucket.End.Sub(bucket.Start))
	}
}

func TestSessionClockDailyBucketIsTradingDay(t *testing.T) {
	clock := stockClock(t, 24*time.Hour)
	bucket, ok, err := clock.BucketAt(utc("2026-10-21T02:00:00Z"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, utc("2026-10-21T00:00:00Z"), bucket.Key, "日线键是交易日期的 UTC 零点")
	require.Equal(t, utc("2026-10-21T01:30:00Z"), bucket.Start)
	require.Equal(t, utc("2026-10-21T07:00:00Z"), bucket.End)
	times, err := clock.SourceTimes(bucket, time.Minute)
	require.NoError(t, err)
	require.Len(t, times, 240, "A 股一个交易日 240 根 1m")
	hourly, err := clock.SourceTimes(bucket, time.Hour)
	require.NoError(t, err)
	require.Len(t, hourly, 4)
	next, err := clock.Next(bucket)
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-22T00:00:00Z"), next.Key)
}

func TestSessionClockSourceTimesExcludeLunch(t *testing.T) {
	clock := stockClock(t, time.Hour)
	bucket, _, _ := clock.BucketAt(utc("2026-10-21T02:45:00Z"))
	times, err := clock.SourceTimes(bucket, 15*time.Minute)
	require.NoError(t, err)
	require.Equal(t, []time.Time{
		utc("2026-10-21T02:30:00Z"), utc("2026-10-21T02:45:00Z"), utc("2026-10-21T03:00:00Z"), utc("2026-10-21T03:15:00Z"),
	}, times)
}

func TestSessionClockRejectsPeriodsThatDoNotDivideSessions(t *testing.T) {
	for _, period := range []time.Duration{45 * time.Minute, 4 * time.Hour, 90 * time.Minute, 16 * time.Minute} {
		_, err := ClockFor("stockcn", period)
		require.ErrorIs(t, err, ErrPeriodNotDivisible, period.String())
		require.ErrorContains(t, err, "09:30–11:30")
	}
	for _, period := range []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 24 * time.Hour} {
		_, err := ClockFor("stockcn", period)
		require.NoError(t, err, period.String())
	}
	clock := stockClock(t, time.Hour)
	require.ErrorIs(t, clock.ValidateSource(45*time.Minute), ErrPeriodNotDivisible)
	require.ErrorIs(t, clock.ValidateSource(2*time.Hour), ErrPeriodNotDivisible)
	require.NoError(t, clock.ValidateSource(time.Minute))
}

func TestSessionClockOutOfCoverageIsAnError(t *testing.T) {
	clock := stockClock(t, time.Hour)
	_, err := clock.LatestClosed(utc("2031-01-04T08:00:00Z"))
	require.True(t, errors.Is(err, ErrOutOfCoverage), "%v", err)
}

func TestMarketForAndAllowedIntraday(t *testing.T) {
	crypto, err := MarketFor("crypto")
	require.NoError(t, err)
	require.Equal(t, AlignmentEpochUTC, crypto.Alignment)
	allowed := crypto.AllowedIntraday()
	require.Contains(t, allowed, "45m")
	require.Contains(t, allowed, "12h")
	require.NotContains(t, allowed, "7m")

	stock, err := MarketFor("stockcn")
	require.NoError(t, err)
	require.Equal(t, AlignmentSession, stock.Alignment)
	require.Equal(t, []string{"1m", "2m", "3m", "4m", "5m", "6m", "8m", "10m", "12m", "15m", "20m", "24m", "30m", "40m", "1h", "2h"}, stock.AllowedIntraday())

	_, err = MarketFor("stockhk")
	require.ErrorIs(t, err, ErrUnknownMarket)
}
