package barclock

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
	"github.com/stretchr/testify/require"
)

var hkSessions = []string{"09:30-12:00", "13:00-16:00"}

// hkClock 用港股日历（有提前收市日）直接构造时钟。时刻都按香港时间写（UTC+8，没有夏令时）。
func hkClock(t testing.TB, period time.Duration) Clock {
	t.Helper()
	calendar, err := marketcalendar.Load("hk_stock")
	require.NoError(t, err)
	location, err := time.LoadLocation("Asia/Hong_Kong")
	require.NoError(t, err)
	clock, err := NewSession(calendar, location, hkSessions, period)
	require.NoError(t, err)
	return clock
}

func hkt(value string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04-07:00", value+"+08:00")
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

// A 股没有提前收市日：2026 全年每一根 5m 的序号等于“交易日序号 × 48 + 日内序号”，桶边界是 09:30 起 24 根、13:00 起 24 根。
func TestStockCNOrdinalAndBucketsUnchanged(t *testing.T) {
	clock, err := ClockFor("stockcn", 5*time.Minute)
	require.NoError(t, err)
	calendar, err := marketcalendar.Load("cn_stock")
	require.NoError(t, err)
	require.Empty(t, calendar.EarlyCloseDates())
	location, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)

	days := 0
	for day := time.Date(2026, 1, 1, 0, 0, 0, 0, location); day.Year() == 2026; day = day.AddDate(0, 0, 1) {
		date, err := marketcalendar.NewCivilDate(day.Year(), day.Month(), day.Day())
		require.NoError(t, err)
		index, err := calendar.TradingDayIndex(date)
		buckets, betweenErr := clock.Between(day, day.AddDate(0, 0, 1), 1000)
		require.NoError(t, betweenErr)
		if errors.Is(err, marketcalendar.ErrNotTradingDay) {
			require.Empty(t, buckets, "%s 不是交易日", date)
			continue
		}
		require.NoError(t, err)
		days++
		require.Len(t, buckets, 48, "%s", date)
		for i, bucket := range buckets {
			open := day.Add(9*time.Hour + 30*time.Minute + time.Duration(i)*5*time.Minute)
			if i >= 24 {
				open = day.Add(13*time.Hour + time.Duration(i-24)*5*time.Minute)
			}
			require.Equal(t, Bucket{Key: open.UTC(), Start: open.UTC(), End: open.UTC().Add(5 * time.Minute)}, bucket, "%s 第 %d 根", date, i)
			ordinal, err := clock.Ordinal(bucket)
			require.NoError(t, err)
			require.Equal(t, int64(index)*48+int64(i), ordinal, "%s 第 %d 根", date, i)
			back, err := clock.BucketAtOrdinal(ordinal)
			require.NoError(t, err)
			require.Equal(t, bucket, back)
		}
	}
	require.Equal(t, 242, days, "2026 年 A 股交易日数")
}

// 提前收市日收市之后没有桶：2026-12-24（圣诞前夕）只有上午 30 根，下一根是 12-28 的 09:30（12-25 休市）。
func TestEarlyCloseDayHasNoBucketsAfterClose(t *testing.T) {
	clock := hkClock(t, 5*time.Minute)

	normal, err := clock.Between(hkt("2026-12-23 00:00"), hkt("2026-12-24 00:00"), 1000)
	require.NoError(t, err)
	require.Len(t, normal, 66)

	half, err := clock.Between(hkt("2026-12-24 00:00"), hkt("2026-12-25 00:00"), 1000)
	require.NoError(t, err)
	require.Len(t, half, 30)
	require.Equal(t, hkt("2026-12-24 09:30"), half[0].Start)
	require.Equal(t, hkt("2026-12-24 11:55"), half[29].Start)
	require.Equal(t, hkt("2026-12-24 12:00"), half[29].End)

	_, ok, err := clock.BucketAt(hkt("2026-12-24 13:05"))
	require.NoError(t, err)
	require.False(t, ok, "提前收市日下午没有桶")
	_, ok, err = clock.BucketAt(hkt("2026-12-23 13:05"))
	require.NoError(t, err)
	require.True(t, ok)

	latest, err := clock.LatestClosed(hkt("2026-12-24 15:00"))
	require.NoError(t, err)
	require.Equal(t, half[29], latest)

	next, err := clock.Next(half[29])
	require.NoError(t, err)
	require.Equal(t, hkt("2026-12-28 09:30"), next.Start)
	prev, err := clock.Prev(next)
	require.NoError(t, err)
	require.Equal(t, half[29], prev)

	// 按 Key 定位：提前收市日下午的时刻不是桶。
	_, err = clock.Ordinal(Bucket{Key: hkt("2026-12-24 13:00")})
	require.ErrorIs(t, err, ErrNoBucket)

	// 30m 那根 11:30 的源 5m 是 6 根；1d 那根收在 12:00，源 5m 是 30 根。
	wide := hkClock(t, 30*time.Minute)
	last, ok, err := wide.BucketAt(hkt("2026-12-24 11:40"))
	require.NoError(t, err)
	require.True(t, ok)
	times, err := wide.SourceTimes(last, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, times, 6)
	daily := hkClock(t, 24*time.Hour)
	day, ok, err := daily.BucketAt(hkt("2026-12-24 10:00"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, hkt("2026-12-24 09:30"), day.Start)
	require.Equal(t, hkt("2026-12-24 12:00"), day.End)
	times, err = daily.SourceTimes(day, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, times, 30)
}

// 交易序号在整份港股日历上连续：从第一根起逐根 Next，序号依次加 1，BucketAtOrdinal 能换回同一根；
// 提前收市日少掉的 36 根不占序号。
func TestEarlyCloseOrdinalIsContinuous(t *testing.T) {
	clock := hkClock(t, 5*time.Minute)
	calendar, err := marketcalendar.Load("hk_stock")
	require.NoError(t, err)

	bucket, err := clock.BucketAtOrdinal(0)
	require.NoError(t, err)
	require.Equal(t, hkt("2026-01-02 09:30"), bucket.Start)
	var count int64
	for {
		ordinal, err := clock.Ordinal(bucket)
		require.NoError(t, err)
		require.Equal(t, count, ordinal, "%s", bucket.Start)
		back, err := clock.BucketAtOrdinal(count)
		require.NoError(t, err)
		require.Equal(t, bucket, back)
		count++
		next, err := clock.Next(bucket)
		if errors.Is(err, ErrOutOfCoverage) {
			break
		}
		require.NoError(t, err)
		bucket = next
	}
	// 日历最后一天 2027-12-31 是提前收市日，最后一根是 11:55。
	require.Equal(t, hkt("2027-12-31 11:55"), bucket.Start)
	days, err := calendar.TradingDayIndex(calendar.LastDate())
	require.NoError(t, err)
	require.Equal(t, int64(days+1)*66-int64(len(calendar.EarlyCloseDates()))*36, count)
	_, err = clock.BucketAtOrdinal(count)
	require.ErrorIs(t, err, ErrOutOfCoverage)

	// 1d 一天一根，提前收市日也是一根。
	daily := hkClock(t, 24*time.Hour)
	for index := 0; index <= days; index++ {
		day, err := daily.BucketAtOrdinal(int64(index))
		require.NoError(t, err)
		ordinal, err := daily.Ordinal(day)
		require.NoError(t, err)
		require.Equal(t, int64(index), ordinal)
	}
}

// 表建好后，有提前收市日的日历算交易序号也不分配内存。
func TestEarlyCloseOrdinalDoesNotAllocate(t *testing.T) {
	clock := hkClock(t, 5*time.Minute)
	key := hkt("2027-06-01 13:05")
	_, err := clock.Ordinal(Bucket{Key: key})
	require.NoError(t, err)
	allocs := testing.AllocsPerRun(100, func() {
		n, err := clock.Ordinal(Bucket{Key: key})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := clock.BucketAtOrdinal(n); err != nil {
			t.Fatal(err)
		}
	})
	require.Zero(t, allocs)
}

// plainDays 只实现交易日判断和按序号定位，没有提前收市日这项能力。
type plainDays struct {
	calendar marketcalendar.TradingCalendar
}

func (d plainDays) Status(date marketcalendar.CivilDate) (marketcalendar.CoverageStatus, error) {
	return d.calendar.Status(date)
}

func (d plainDays) TradingDayIndex(date marketcalendar.CivilDate) (int, error) {
	return d.calendar.TradingDayIndex(date)
}

func (d plainDays) TradingDayAt(index int) (marketcalendar.CivilDate, error) {
	return d.calendar.TradingDayAt(index)
}

// 交易日历没有实现 EarlyCloses 时，每个交易日的时段都相同。
func TestCalendarWithoutEarlyClosesKeepsFullDays(t *testing.T) {
	calendar, err := marketcalendar.Load("hk_stock")
	require.NoError(t, err)
	location, err := time.LoadLocation("Asia/Hong_Kong")
	require.NoError(t, err)
	clock, err := NewSession(plainDays{calendar}, location, hkSessions, 5*time.Minute)
	require.NoError(t, err)
	buckets, err := clock.Between(hkt("2026-12-24 00:00"), hkt("2026-12-25 00:00"), 1000)
	require.NoError(t, err)
	require.Len(t, buckets, 66)
	first, err := clock.Ordinal(buckets[0])
	require.NoError(t, err)
	index, err := calendar.TradingDayIndex(mustCivil(t, "2026-12-24"))
	require.NoError(t, err)
	require.Equal(t, int64(index)*66, first)
}

func mustCivil(t testing.TB, value string) marketcalendar.CivilDate {
	t.Helper()
	date, err := marketcalendar.ParseCivilDate(value)
	require.NoError(t, err)
	return date
}

// 交易时段判断也按提前收市日当天实际的时段算。
func TestHoursOnEarlyCloseDay(t *testing.T) {
	calendar, err := marketcalendar.Load("hk_stock")
	require.NoError(t, err)
	location, err := time.LoadLocation("Asia/Hong_Kong")
	require.NoError(t, err)
	hours, err := NewHours(calendar, location, hkSessions)
	require.NoError(t, err)

	in, err := hours.InSession(hkt("2026-12-24 11:59"))
	require.NoError(t, err)
	require.True(t, in)
	in, err = hours.InSession(hkt("2026-12-24 13:30"))
	require.NoError(t, err)
	require.False(t, in)
	in, err = hours.InSession(hkt("2026-12-23 13:30"))
	require.NoError(t, err)
	require.True(t, in)

	closeAt, err := hours.LastClose(hkt("2026-12-24 18:00"))
	require.NoError(t, err)
	require.Equal(t, hkt("2026-12-24 12:00"), closeAt)
	open, err := hours.NextOpen(hkt("2026-12-24 12:30"))
	require.NoError(t, err)
	require.Equal(t, hkt("2026-12-28 09:30"), open)
}

// 构造时钟不建累计序号表：Monitor 每次取时钟都新建一个。
func BenchmarkNewSession(b *testing.B) {
	calendar, err := marketcalendar.Load("cn_stock")
	require.NoError(b, err)
	location, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(b, err)
	sessions := []string{"09:30-11:30", "13:00-15:00"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := NewSession(calendar, location, sessions, 5*time.Minute); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEarlyCloseOrdinalRoundTrip(b *testing.B) {
	clock := hkClock(b, 5*time.Minute)
	key := hkt("2027-06-01 13:05")
	b.ReportAllocs()
	for b.Loop() {
		n, err := clock.Ordinal(Bucket{Key: key})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := clock.BucketAtOrdinal(n); err != nil {
			b.Fatal(err)
		}
	}
}
