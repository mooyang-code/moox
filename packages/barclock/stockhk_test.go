package barclock

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
	"github.com/stretchr/testify/require"
)

func TestStockHKMarket(t *testing.T) {
	market, err := MarketFor("stockhk")
	require.NoError(t, err)
	require.Equal(t, Market{
		ID: "stockhk", Alignment: AlignmentSession, CalendarID: "hk_stock", Timezone: "Asia/Hong_Kong",
		Sessions: []string{"09:30-12:00", "13:00-16:00"},
	}, market)

	// 日内周期要整除上午 150 分钟和下午 180 分钟：5m、15m、30m 可以，1h 不行。
	allowed := market.AllowedIntraday()
	require.Equal(t, []string{"1m", "2m", "3m", "5m", "6m", "10m", "15m", "30m"}, allowed)
	_, err = market.Clock(time.Hour)
	require.ErrorIs(t, err, ErrPeriodNotDivisible)
}

// 普通交易日 5m 是 66 根：09:30～11:55 共 30 根，13:00～15:55 共 36 根，12:00～13:00 没有桶。
func TestStockHKNormalDayBuckets(t *testing.T) {
	clock, err := ClockFor("stockhk", 5*time.Minute)
	require.NoError(t, err)
	// 2026-11-12 是周四。
	buckets, err := clock.Between(hkt("2026-11-12 00:00"), hkt("2026-11-13 00:00"), 1000)
	require.NoError(t, err)
	require.Len(t, buckets, 66)
	require.Equal(t, hkt("2026-11-12 09:30"), buckets[0].Start)
	require.Equal(t, hkt("2026-11-12 11:55"), buckets[29].Start)
	require.Equal(t, hkt("2026-11-12 13:00"), buckets[30].Start)
	require.Equal(t, hkt("2026-11-12 15:55"), buckets[65].Start)
	require.Equal(t, hkt("2026-11-12 16:00"), buckets[65].End)
	for _, bucket := range buckets {
		require.Equal(t, bucket.Start, bucket.Key)
		require.Equal(t, 5*time.Minute, bucket.End.Sub(bucket.Start))
	}

	// 桶的键是开盘时刻：数据源给的收盘标签 12:00、13:05、16:00 分别对应开盘 11:55、13:00、15:55 这三根。
	for _, test := range []struct{ closeLabel, key string }{
		{"2026-11-12 12:00", "2026-11-12 11:55"},
		{"2026-11-12 13:05", "2026-11-12 13:00"},
		{"2026-11-12 16:00", "2026-11-12 15:55"},
	} {
		bucket, err := clock.LatestClosed(hkt(test.closeLabel))
		require.NoError(t, err)
		require.Equal(t, hkt(test.key), bucket.Key, "收盘标签 %s", test.closeLabel)
		require.Equal(t, hkt(test.closeLabel), bucket.End)
	}
	// 12:00、16:00 这两个时刻已经收市，午休里也没有桶；13:00 是下午第一根的开盘。
	for _, at := range []string{"2026-11-12 12:00", "2026-11-12 12:30", "2026-11-12 16:00", "2026-11-12 09:29"} {
		_, ok, err := clock.BucketAt(hkt(at))
		require.NoError(t, err)
		require.False(t, ok, at)
	}
	afternoon, ok, err := clock.BucketAt(hkt("2026-11-12 13:00"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, buckets[30], afternoon)
	// 午休前后相邻；13:00 这一刻最新收盘的还是 11:55 那根。
	next, err := clock.Next(buckets[29])
	require.NoError(t, err)
	require.Equal(t, buckets[30], next)
	latest, err := clock.LatestClosed(hkt("2026-11-12 13:00"))
	require.NoError(t, err)
	require.Equal(t, buckets[29], latest)
}

// 半日市 2026-12-24 只有上午 30 根，最后一根 11:55（收盘标签 12:00）。
func TestStockHKHalfDayBuckets(t *testing.T) {
	clock, err := ClockFor("stockhk", 5*time.Minute)
	require.NoError(t, err)
	buckets, err := clock.Between(hkt("2026-12-24 00:00"), hkt("2026-12-25 00:00"), 1000)
	require.NoError(t, err)
	require.Len(t, buckets, 30)
	require.Equal(t, hkt("2026-12-24 09:30"), buckets[0].Start)
	require.Equal(t, hkt("2026-12-24 11:55"), buckets[29].Start)
	require.Equal(t, hkt("2026-12-24 12:00"), buckets[29].End)

	for _, at := range []string{"2026-12-24 13:00", "2026-12-24 13:05", "2026-12-24 15:55"} {
		_, ok, err := clock.BucketAt(hkt(at))
		require.NoError(t, err)
		require.False(t, ok, at)
	}
	// 当天 16:00 最新收盘的仍是 11:55 那根。
	latest, err := clock.LatestClosed(hkt("2026-12-24 16:00"))
	require.NoError(t, err)
	require.Equal(t, buckets[29], latest)

	// 15m、30m 当天是 10 根、5 根。
	for period, want := range map[time.Duration]int{15 * time.Minute: 10, 30 * time.Minute: 5} {
		wide, err := ClockFor("stockhk", period)
		require.NoError(t, err)
		got, err := wide.Between(hkt("2026-12-24 00:00"), hkt("2026-12-25 00:00"), 1000)
		require.NoError(t, err)
		require.Len(t, got, want, "%s", period)
	}
}

// 交易序号往返：半日市前后、周末、节假日、日历首尾。
func TestStockHKOrdinalRoundTrip(t *testing.T) {
	clock, err := ClockFor("stockhk", 5*time.Minute)
	require.NoError(t, err)
	ordinal := func(at string) int64 {
		t.Helper()
		n, err := clock.Ordinal(Bucket{Key: hkt(at)})
		require.NoError(t, err, at)
		back, err := clock.BucketAtOrdinal(n)
		require.NoError(t, err, at)
		require.Equal(t, hkt(at), back.Key, at)
		return n
	}

	// 日历首尾：第一根是 2026-01-02 09:30，序号 0；最后一根是 2027-12-31（半日市）11:55。
	require.Zero(t, ordinal("2026-01-02 09:30"))
	last := ordinal("2027-12-31 11:55")
	_, err = clock.BucketAtOrdinal(last + 1)
	require.ErrorIs(t, err, ErrOutOfCoverage)
	_, err = clock.Ordinal(Bucket{Key: hkt("2025-12-31 09:30")})
	require.ErrorIs(t, err, ErrOutOfCoverage)

	// 午休相邻；普通交易日一天 66 根。
	require.Equal(t, ordinal("2026-11-12 11:55")+1, ordinal("2026-11-12 13:00"))
	require.Equal(t, ordinal("2026-11-12 09:30")+66, ordinal("2026-11-13 09:30"))
	// 周末：周五最后一根之后是周一第一根。
	require.Equal(t, ordinal("2026-11-13 15:55")+1, ordinal("2026-11-16 09:30"))
	// 节假日：2026-09-30 之后是 10-02（10-01 国庆日休市）。
	require.Equal(t, ordinal("2026-09-30 15:55")+1, ordinal("2026-10-02 09:30"))
	// 半日市前后：12-23 最后一根 → 12-24 第一根；12-24 只占 30 个序号，之后是 12-28（12-25 休市、周末）。
	require.Equal(t, ordinal("2026-12-23 15:55")+1, ordinal("2026-12-24 09:30"))
	require.Equal(t, ordinal("2026-12-24 09:30")+29, ordinal("2026-12-24 11:55"))
	require.Equal(t, ordinal("2026-12-24 11:55")+1, ordinal("2026-12-28 09:30"))
	// 连续两个半日市之间（12-24、12-31）和之后的序号都对得上。
	require.Equal(t, ordinal("2026-12-28 09:30")+3*66, ordinal("2026-12-31 09:30"))
	require.Equal(t, ordinal("2026-12-31 11:55")+1, ordinal("2027-01-04 09:30"))

	// 半日市下午、午休、休市日都不是桶。
	for _, at := range []string{"2026-12-24 13:00", "2026-12-24 15:55", "2026-11-12 12:00", "2026-12-25 09:30", "2026-11-14 09:30"} {
		_, err := clock.Ordinal(Bucket{Key: hkt(at)})
		require.ErrorIs(t, err, ErrNoBucket, at)
	}

	// 1d：半日市也是一根，收在 12:00。
	daily, err := ClockFor("stockhk", 24*time.Hour)
	require.NoError(t, err)
	day, ok, err := daily.BucketAt(hkt("2026-12-24 10:00"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 12, 24, 0, 0, 0, 0, time.UTC), day.Key)
	require.Equal(t, hkt("2026-12-24 12:00"), day.End)
	n, err := daily.Ordinal(day)
	require.NoError(t, err)
	next, err := daily.BucketAtOrdinal(n + 1)
	require.NoError(t, err)
	require.Equal(t, hkt("2026-12-28 16:00"), next.End)
}

// 每个登记的交易时段市场都能加载日历、构造 5m 和 1d 的时钟。
func TestEverySessionMarketBuildsClocks(t *testing.T) {
	for _, market := range Markets() {
		if market.Alignment != AlignmentSession {
			continue
		}
		_, err := marketcalendar.Load(market.CalendarID)
		require.NoError(t, err, market.ID)
		for _, period := range []time.Duration{5 * time.Minute, 24 * time.Hour} {
			_, err := market.Clock(period)
			require.NoError(t, err, "%s %s", market.ID, period)
		}
		_, err = market.Hours()
		require.NoError(t, err, market.ID)
	}
}
