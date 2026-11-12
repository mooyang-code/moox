package barclock

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// nyt 把“纽约当地时间”写法转成时刻（UTC）：夏令时与冬令时都由时区库换算。
func nyt(value string) time.Time {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, location)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

func TestStockUSMarket(t *testing.T) {
	market, err := MarketFor("stockus")
	require.NoError(t, err)
	require.Equal(t, Market{
		ID: "stockus", Alignment: AlignmentSession, CalendarID: "us_stock", Timezone: "America/New_York",
		Sessions: []string{"09:30-16:00"},
	}, market)

	// 日内周期要整除 390 分钟的交易时段：5m、15m、30m 可以，1h 不行（390 不能被 60 整除）。
	allowed := market.AllowedIntraday()
	require.Equal(t, []string{"1m", "2m", "3m", "5m", "6m", "10m", "15m", "30m"}, allowed)
	for _, period := range []time.Duration{5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 24 * time.Hour} {
		_, err := market.Clock(period)
		require.NoError(t, err, "%s", period)
	}
	_, err = market.Clock(time.Hour)
	require.ErrorIs(t, err, ErrPeriodNotDivisible)
	_, err = market.Hours()
	require.NoError(t, err)
}

// 普通交易日 5m 是 78 根，09:30 起到 15:55，没有午休。
func TestStockUSNormalDayBuckets(t *testing.T) {
	clock, err := ClockFor("stockus", 5*time.Minute)
	require.NoError(t, err)
	// 2026-11-12 是周四，冬令时。
	buckets, err := clock.Between(nyt("2026-11-12 00:00"), nyt("2026-11-13 00:00"), 1000)
	require.NoError(t, err)
	require.Len(t, buckets, 78)
	require.Equal(t, nyt("2026-11-12 09:30"), buckets[0].Start)
	require.Equal(t, nyt("2026-11-12 15:55"), buckets[77].Start)
	require.Equal(t, nyt("2026-11-12 16:00"), buckets[77].End)
	for _, bucket := range buckets {
		require.Equal(t, bucket.Start, bucket.Key)
		require.Equal(t, 5*time.Minute, bucket.End.Sub(bucket.Start))
	}
	// 冬令时 09:30 是 14:30 UTC，夏令时是 13:30 UTC。
	require.Equal(t, time.Date(2026, 11, 12, 14, 30, 0, 0, time.UTC), buckets[0].Start)
	summer, err := clock.Between(nyt("2026-07-15 00:00"), nyt("2026-07-16 00:00"), 1000)
	require.NoError(t, err)
	require.Len(t, summer, 78)
	require.Equal(t, time.Date(2026, 7, 15, 13, 30, 0, 0, time.UTC), summer[0].Start)

	// 桶的键是开盘时刻：数据源给的收盘标签 09:35、13:00、16:00 分别对应开盘 09:30、12:55、15:55 这三根。
	for _, test := range []struct{ closeLabel, key string }{
		{"2026-11-12 09:35", "2026-11-12 09:30"},
		{"2026-11-12 13:00", "2026-11-12 12:55"},
		{"2026-11-12 16:00", "2026-11-12 15:55"},
	} {
		bucket, err := clock.LatestClosed(nyt(test.closeLabel))
		require.NoError(t, err)
		require.Equal(t, nyt(test.key), bucket.Key, "收盘标签 %s", test.closeLabel)
		require.Equal(t, nyt(test.closeLabel), bucket.End)
	}
	// 16:00 与 09:29 不在任何桶里；13:00 是一根桶的开盘，中午没有休息。
	for _, at := range []string{"2026-11-12 16:00", "2026-11-12 09:29", "2026-11-12 18:00"} {
		_, ok, err := clock.BucketAt(nyt(at))
		require.NoError(t, err)
		require.False(t, ok, at)
	}
	noon, ok, err := clock.BucketAt(nyt("2026-11-12 13:00"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, nyt("2026-11-12 13:00"), noon.Key)
	prev, err := clock.Prev(noon)
	require.NoError(t, err)
	require.Equal(t, nyt("2026-11-12 12:55"), prev.Key)
	// 周五最后一根之后是下周一第一根。
	last, err := clock.LatestClosed(nyt("2026-11-13 16:00"))
	require.NoError(t, err)
	next, err := clock.Next(last)
	require.NoError(t, err)
	require.Equal(t, nyt("2026-11-16 09:30"), next.Key)

	// 15m、30m 当天是 26 根、13 根。
	for period, want := range map[time.Duration]int{15 * time.Minute: 26, 30 * time.Minute: 13} {
		wide, err := ClockFor("stockus", period)
		require.NoError(t, err)
		got, err := wide.Between(nyt("2026-11-12 00:00"), nyt("2026-11-13 00:00"), 1000)
		require.NoError(t, err)
		require.Len(t, got, want, "%s", period)
	}
}

// 半日市 13:00 收市：当天 42 根，最后一根 12:55（收盘标签 13:00），13:00 之后没有桶。
func TestStockUSHalfDayBuckets(t *testing.T) {
	clock, err := ClockFor("stockus", 5*time.Minute)
	require.NoError(t, err)
	for _, day := range []string{"2026-11-27", "2026-12-24", "2027-11-26"} {
		buckets, err := clock.Between(nyt(day+" 00:00"), nyt(day+" 23:59"), 1000)
		require.NoError(t, err, day)
		require.Len(t, buckets, 42, day)
		require.Equal(t, nyt(day+" 09:30"), buckets[0].Start, day)
		require.Equal(t, nyt(day+" 12:55"), buckets[41].Start, day)
		require.Equal(t, nyt(day+" 13:00"), buckets[41].End, day)

		for _, at := range []string{" 13:00", " 13:05", " 15:55"} {
			_, ok, err := clock.BucketAt(nyt(day + at))
			require.NoError(t, err)
			require.False(t, ok, day+at)
		}
		// 当天 16:00 最新收盘的仍是 12:55 那根。
		latest, err := clock.LatestClosed(nyt(day + " 16:00"))
		require.NoError(t, err)
		require.Equal(t, buckets[41], latest, day)
	}

	// 15m 当天 14 根，30m 当天 7 根，末根都收在 13:00。
	for period, want := range map[time.Duration]int{15 * time.Minute: 14, 30 * time.Minute: 7} {
		wide, err := ClockFor("stockus", period)
		require.NoError(t, err)
		got, err := wide.Between(nyt("2026-11-27 00:00"), nyt("2026-11-28 00:00"), 1000)
		require.NoError(t, err)
		require.Len(t, got, want, "%s", period)
		require.Equal(t, nyt("2026-11-27 13:00"), got[len(got)-1].End, "%s", period)
	}

	// 2027 年平安夜休市，不是半日市；2027-12-31 照常交易。
	for day, want := range map[string]int{"2027-12-24": 0, "2027-12-23": 78, "2027-12-31": 78, "2026-07-02": 78, "2027-07-02": 78} {
		got, err := clock.Between(nyt(day+" 00:00"), nyt(day+" 23:59"), 1000)
		require.NoError(t, err, day)
		require.Len(t, got, want, day)
	}
}

// 夏令时切换前后（美国夏令时 2026-03-08 起、2026-11-01 止，2027-03-14 起、2027-11-07 止），每天都是 78 根，
// 且当地 09:30 开盘、16:00 收盘；UTC 偏移按时区库核对。
func TestStockUSDaylightSavingDays(t *testing.T) {
	clock, err := ClockFor("stockus", 5*time.Minute)
	require.NoError(t, err)
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	for _, test := range []struct {
		day      string
		offsetH  int // 相对 UTC 的小时偏移（-5 冬令时，-4 夏令时）
		openUTC  string
		closeUTC string
	}{
		{"2026-03-06", -5, "14:30", "21:00"},
		{"2026-03-09", -4, "13:30", "20:00"},
		{"2026-10-30", -4, "13:30", "20:00"},
		{"2026-11-02", -5, "14:30", "21:00"},
		{"2027-03-12", -5, "14:30", "21:00"},
		{"2027-03-15", -4, "13:30", "20:00"},
		{"2027-11-05", -4, "13:30", "20:00"},
		{"2027-11-08", -5, "14:30", "21:00"},
	} {
		_, offset := nyt(test.day + " 12:00").In(location).Zone()
		require.Equal(t, test.offsetH*3600, offset, "%s 的 UTC 偏移", test.day)

		buckets, err := clock.Between(nyt(test.day+" 00:00"), nyt(test.day+" 23:59"), 1000)
		require.NoError(t, err, test.day)
		require.Len(t, buckets, 78, test.day)
		first, last := buckets[0], buckets[77]
		require.Equal(t, nyt(test.day+" 09:30"), first.Start, test.day)
		require.Equal(t, nyt(test.day+" 16:00"), last.End, test.day)
		require.Equal(t, test.day+" "+test.openUTC, first.Start.Format("2006-01-02 15:04"), test.day)
		require.Equal(t, test.day+" "+test.closeUTC, last.End.Format("2006-01-02 15:04"), test.day)
		require.Equal(t, 9, first.Start.In(location).Hour(), test.day)
		require.Equal(t, 30, first.Start.In(location).Minute(), test.day)
		for _, bucket := range buckets {
			require.Equal(t, 5*time.Minute, bucket.End.Sub(bucket.Start), test.day)
		}
	}

	// 切换周末前后的序号连续：周五最后一根 +1 就是周一第一根，每天占 78 个序号。
	ordinal := func(at string) int64 {
		t.Helper()
		n, err := clock.Ordinal(Bucket{Key: nyt(at)})
		require.NoError(t, err, at)
		return n
	}
	for _, pair := range [][2]string{{"2026-03-06", "2026-03-09"}, {"2026-10-30", "2026-11-02"}, {"2027-03-12", "2027-03-15"}, {"2027-11-05", "2027-11-08"}} {
		require.Equal(t, ordinal(pair[0]+" 15:55")+1, ordinal(pair[1]+" 09:30"), pair[0])
		require.Equal(t, ordinal(pair[0]+" 09:30")+77, ordinal(pair[0]+" 15:55"), pair[0])
	}

	// 交易时段判断：切换后的周一 09:30 开盘、09:29 还没开。
	hours, err := mustMarket(t).Hours()
	require.NoError(t, err)
	for _, day := range []string{"2026-03-09", "2026-11-02", "2027-03-15", "2027-11-08"} {
		in, err := hours.InSession(nyt(day + " 09:30"))
		require.NoError(t, err)
		require.True(t, in, day)
		in, err = hours.InSession(nyt(day + " 09:29"))
		require.NoError(t, err)
		require.False(t, in, day)
		in, err = hours.InSession(nyt(day + " 16:00"))
		require.NoError(t, err)
		require.False(t, in, day)
	}
	// 半日市 13:00 之后不在交易时段。
	in, err := hours.InSession(nyt("2026-11-27 12:59"))
	require.NoError(t, err)
	require.True(t, in)
	in, err = hours.InSession(nyt("2026-11-27 13:00"))
	require.NoError(t, err)
	require.False(t, in)
}

func mustMarket(t testing.TB) Market {
	t.Helper()
	market, err := MarketFor("stockus")
	require.NoError(t, err)
	return market
}

// 交易序号在整个覆盖期内连续、单调，Ordinal 与 BucketAtOrdinal 互逆；总数与每日根数对得上。
func TestStockUSOrdinalExhaustive(t *testing.T) {
	for _, test := range []struct {
		period time.Duration
		normal int // 普通交易日的根数
		half   int // 半日市的根数
	}{
		{time.Minute, 390, 210},
		{3 * time.Minute, 130, 70},
		{5 * time.Minute, 78, 42},
		{15 * time.Minute, 26, 14},
		{30 * time.Minute, 13, 7},
		{24 * time.Hour, 1, 1},
	} {
		clock, err := ClockFor("stockus", test.period)
		require.NoError(t, err, "%s", test.period)

		var (
			count  int64
			prev   Bucket
			perDay = map[string]int{}
		)
		location, err := time.LoadLocation("America/New_York")
		require.NoError(t, err)
		for n := int64(0); ; n++ {
			bucket, err := clock.BucketAtOrdinal(n)
			if err != nil {
				require.ErrorIs(t, err, ErrOutOfCoverage, "%s 序号 %d", test.period, n)
				break
			}
			got, err := clock.Ordinal(bucket)
			require.NoError(t, err, "%s 序号 %d", test.period, n)
			require.Equal(t, n, got, "%s", test.period)
			if n > 0 {
				require.True(t, bucket.Key.After(prev.Key), "%s 序号 %d 的键没有递增", test.period, n)
				require.False(t, bucket.Start.Before(prev.End), "%s 序号 %d 与前一根重叠", test.period, n)
				if test.period < 24*time.Hour {
					// 周期内相邻的两根首尾相接；跨天时才会有间隔。
					if bucket.Start.In(location).Day() == prev.Start.In(location).Day() {
						require.Equal(t, prev.End, bucket.Start, "%s 序号 %d", test.period, n)
					}
				}
			}
			perDay[bucket.Start.In(location).Format("2006-01-02")]++
			prev = bucket
			count++
		}
		require.Equal(t, 502, len(perDay), "%s 覆盖期内的交易日数", test.period)
		half := map[string]bool{"2026-11-27": true, "2026-12-24": true, "2027-11-26": true}
		var total int64
		for day, got := range perDay {
			want := test.normal
			if half[day] && test.period < 24*time.Hour {
				want = test.half
			}
			require.Equal(t, want, got, "%s %s", test.period, day)
			total += int64(got)
		}
		require.Equal(t, total, count, "%s", test.period)
		// 覆盖期之后与之前都明确报超出。
		_, err = clock.BucketAtOrdinal(count)
		require.ErrorIs(t, err, ErrOutOfCoverage)
		_, err = clock.BucketAtOrdinal(-1)
		require.ErrorIs(t, err, ErrOutOfCoverage)
	}
}

// 连续两个半日市前后的序号：感恩节（休市）→ 次日半日市 → 周一；平安夜半日市 → 圣诞节（休市）→ 周一。
func TestStockUSOrdinalAroundHalfDays(t *testing.T) {
	clock, err := ClockFor("stockus", 5*time.Minute)
	require.NoError(t, err)
	ordinal := func(at string) int64 {
		t.Helper()
		n, err := clock.Ordinal(Bucket{Key: nyt(at)})
		require.NoError(t, err, at)
		back, err := clock.BucketAtOrdinal(n)
		require.NoError(t, err, at)
		require.Equal(t, nyt(at), back.Key, at)
		return n
	}

	// 日历首尾：第一根是 2026-01-02 09:30，序号 0；最后一根是 2027-12-31 15:55。
	require.Zero(t, ordinal("2026-01-02 09:30"))
	last := ordinal("2027-12-31 15:55")
	_, err = clock.BucketAtOrdinal(last + 1)
	require.ErrorIs(t, err, ErrOutOfCoverage)
	_, err = clock.Ordinal(Bucket{Key: nyt("2025-12-31 09:30")})
	require.ErrorIs(t, err, ErrOutOfCoverage)

	// 2026-11-26 感恩节休市，11-27 半日市 42 根，之后是周一 11-30。
	require.Equal(t, ordinal("2026-11-25 15:55")+1, ordinal("2026-11-27 09:30"))
	require.Equal(t, ordinal("2026-11-27 09:30")+41, ordinal("2026-11-27 12:55"))
	require.Equal(t, ordinal("2026-11-27 12:55")+1, ordinal("2026-11-30 09:30"))
	// 2026-12-24 半日市，12-25 休市，之后是周一 12-28。
	require.Equal(t, ordinal("2026-12-23 15:55")+1, ordinal("2026-12-24 09:30"))
	require.Equal(t, ordinal("2026-12-24 09:30")+41, ordinal("2026-12-24 12:55"))
	require.Equal(t, ordinal("2026-12-24 12:55")+1, ordinal("2026-12-28 09:30"))
	// 两个半日市之间是 18 个整天（11-30 起的三整周加 12-21～12-23），每天 78 根。
	require.Equal(t, ordinal("2026-11-30 09:30")+78*18, ordinal("2026-12-24 09:30"))
	// 2027-11-26 半日市前后。
	require.Equal(t, ordinal("2027-11-24 15:55")+1, ordinal("2027-11-26 09:30"))
	require.Equal(t, ordinal("2027-11-26 12:55")+1, ordinal("2027-11-29 09:30"))

	// 半日市下午、休市日、周末都不是桶。
	for _, at := range []string{"2026-11-27 13:00", "2026-11-27 15:55", "2026-12-24 13:00", "2026-11-26 09:30", "2026-12-25 09:30", "2026-11-28 09:30", "2027-12-24 09:30"} {
		_, err := clock.Ordinal(Bucket{Key: nyt(at)})
		require.ErrorIs(t, err, ErrNoBucket, at)
	}

	// 1d：半日市也是一根，收在 13:00。
	daily, err := ClockFor("stockus", 24*time.Hour)
	require.NoError(t, err)
	day, ok, err := daily.BucketAt(nyt("2026-11-27 10:00"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC), day.Key)
	require.Equal(t, nyt("2026-11-27 09:30"), day.Start)
	require.Equal(t, nyt("2026-11-27 13:00"), day.End)
	n, err := daily.Ordinal(day)
	require.NoError(t, err)
	next, err := daily.BucketAtOrdinal(n + 1)
	require.NoError(t, err)
	require.Equal(t, nyt("2026-11-30 16:00"), next.End)
}
