package input

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// stockMarketID 是 A 股在 barclock 里登记的市场（空间）ID。
const stockMarketID = "stockcn"

// stockSessionClock 是 A 股日内周期：桶由 barclock 按交易时段切分（不跨午休），bar_start 是真实的开盘时刻（UTC），
// bar_end 是 bar_start + 周期；BarIndex 是 barclock 的交易序号，午休、隔夜、休市不占根数，与因子的交易序号轴一致。
type stockSessionClock struct {
	clock     barclock.Clock
	frequency string
	period    time.Duration
	calendar  marketcalendar.TradingCalendar
	location  *time.Location
	perDay    int
}

// stockSessionClocks 缓存各日内周期的时钟（频率 → *stockSessionClock）：时钟不可变，构造时要扫一遍交易时段。
var stockSessionClocks sync.Map

// stockSessionClockFor 返回 A 股日内周期的时钟；周期不在 barclock 允许的日内周期之内时返回 ErrUnsupportedCalendar。
func stockSessionClockFor(frequency string) (periodClock, error) {
	if cached, ok := stockSessionClocks.Load(frequency); ok {
		return cached.(*stockSessionClock), nil
	}
	market, err := barclock.MarketFor(stockMarketID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(market.AllowedIntraday(), frequency) {
		return nil, fmt.Errorf("%w：日历 \"cn_stock\" 不支持频率 %q（日内周期须整除每个交易时段：%v）", ErrUnsupportedCalendar, frequency, market.Sessions)
	}
	period, err := parseBarDuration(frequency)
	if err != nil {
		return nil, err
	}
	calendar, location, err := stockCalendar()
	if err != nil {
		return nil, err
	}
	clock, err := barclock.NewSession(calendar, location, market.Sessions, period)
	if err != nil {
		return nil, fmt.Errorf("A 股 %s 的交易时段时钟无法构造：%w", frequency, err)
	}
	// 每个交易日的根数：A 股没有半日市，数日历第一个交易日的桶即可。
	first := calendar.FirstDate()
	dayStart := time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, location)
	buckets, err := clock.Between(dayStart, dayStart.Add(24*time.Hour), 24*60)
	if err != nil || len(buckets) == 0 {
		return nil, fmt.Errorf("A 股 %s 每个交易日的根数无法计算：%v", frequency, err)
	}
	value := &stockSessionClock{clock: clock, frequency: frequency, period: period, calendar: calendar, location: location, perDay: len(buckets)}
	actual, _ := stockSessionClocks.LoadOrStore(frequency, value)
	return actual.(*stockSessionClock), nil
}

// stockIntraday 报告日历与周期是否是受支持的 A 股日内周期。
func stockIntraday(calendarID, frequency string) bool {
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return false
	}
	_, ok := clock.(*stockSessionClock)
	return ok
}

// sessionError 把 barclock 的错误换成带日历覆盖范围的中文说明：barclock 用自己的 ErrOutOfCoverage 包装，
// 交易日历包的错误链在那里断开了。
func (c *stockSessionClock) sessionError(at time.Time, err error) error {
	if errors.Is(err, barclock.ErrOutOfCoverage) {
		err = marketcalendar.ErrOutOfCoverage
	}
	translated := translateCalendarError(err)
	return &describedError{message: fmt.Sprintf("A 股 %s 周期 %s：%s", c.frequency, shanghaiLabel(at, c.location), translated.Error()), cause: err}
}

// shiftError 说明从 at 移动到交易序号 target 时越过了内嵌日历的首尾：与日线一致，用“之前 / 之后没有交易日”的哨兵错误，
// 调用方据此区分“需要追溯到日历起点之前”与其他失败。
func (c *stockSessionClock) shiftError(at time.Time, target int64) error {
	sentinel := marketcalendar.ErrNoNextTradingDay
	if target < 0 {
		sentinel = marketcalendar.ErrNoPreviousTradingDay
	}
	return &describedError{message: fmt.Sprintf("A 股 %s 周期 %s：%s", c.frequency, shanghaiLabel(at, c.location), translateCalendarError(sentinel).Error()), cause: sentinel}
}

// bucketAt 返回交易序号 n 的桶；越过日历首尾时返回 shiftError。
func (c *stockSessionClock) bucketAt(at time.Time, n int64) (barclock.Bucket, error) {
	if n < 0 {
		return barclock.Bucket{}, c.shiftError(at, n)
	}
	bucket, err := c.clock.BucketAtOrdinal(n)
	if errors.Is(err, barclock.ErrOutOfCoverage) {
		return barclock.Bucket{}, c.shiftError(at, n)
	}
	if err != nil {
		return barclock.Bucket{}, c.sessionError(at, err)
	}
	return bucket, nil
}

// boundaries 计算桶的周期边界。上一根与下一根按交易序号取：日历第一根没有上一根、最后一根没有下一根，对应字段留空。
func (c *stockSessionClock) boundaries(bucket barclock.Bucket) (PeriodBoundaries, error) {
	index, err := c.clock.Ordinal(bucket)
	if err != nil {
		return PeriodBoundaries{}, c.sessionError(bucket.Start, err)
	}
	out := PeriodBoundaries{StorageStart: bucket.Start.UTC(), BarEnd: bucket.End.UTC(), BarIndex: index}
	if index > 0 {
		previous, err := c.bucketAt(bucket.Start, index-1)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		out.PreviousStart = previous.Start.UTC()
	}
	next, err := c.clock.BucketAtOrdinal(index + 1)
	switch {
	case err == nil:
		out.NextEnd = next.End.UTC()
	case !errors.Is(err, barclock.ErrOutOfCoverage):
		return PeriodBoundaries{}, c.sessionError(bucket.Start, err)
	}
	return out, nil
}

func (c *stockSessionClock) closed(trigger time.Time) (PeriodBoundaries, error) {
	bucket, err := c.clock.LatestClosed(trigger)
	if err != nil {
		return PeriodBoundaries{}, c.sessionError(trigger, err)
	}
	return c.boundaries(bucket)
}

func (c *stockSessionClock) fromStorageStart(start time.Time) (PeriodBoundaries, error) {
	bucket, ok, err := c.clock.BucketAt(start)
	if err != nil {
		return PeriodBoundaries{}, c.sessionError(start, err)
	}
	if !ok || !bucket.Start.Equal(start) {
		return PeriodBoundaries{}, fmt.Errorf("Storage 周期 %s 不是 A 股交易时段内 %s bar 的起点", shanghaiLabel(start, c.location), c.frequency)
	}
	return c.boundaries(bucket)
}

func (c *stockSessionClock) fromBarEnd(end time.Time) (PeriodBoundaries, error) {
	start := end.Add(-c.period)
	bucket, ok, err := c.clock.BucketAt(start)
	if err != nil {
		return PeriodBoundaries{}, c.sessionError(end, err)
	}
	if !ok || !bucket.Start.Equal(start) {
		return PeriodBoundaries{}, fmt.Errorf("bar_end %s 不是 A 股交易时段内 %s bar 的终点", shanghaiLabel(end, c.location), c.frequency)
	}
	return c.boundaries(bucket)
}

func (c *stockSessionClock) advance(end time.Time, n int) (time.Time, error) {
	period, err := c.fromBarEnd(end)
	if err != nil {
		return time.Time{}, err
	}
	bucket, err := c.bucketAt(end, period.BarIndex+int64(n))
	if err != nil {
		return time.Time{}, err
	}
	return bucket.End.UTC(), nil
}

func (c *stockSessionClock) historyStart(last PeriodBoundaries, count int) (time.Time, error) {
	bucket, err := c.bucketAt(last.StorageStart, last.BarIndex-int64(count-1))
	if err != nil {
		return time.Time{}, err
	}
	return bucket.Start.UTC(), nil
}

// floor 取不晚于 at 的交易时段 bar：at 在某根之内取那一根，在午休、收盘后或休市日取之前最后一根；晚于日历最后一天时
// 夹到日历的最后一根。
func (c *stockSessionClock) floor(at time.Time) (time.Time, error) {
	day, err := localDay(at, c.location)
	if err != nil {
		return time.Time{}, err
	}
	if day.Before(c.calendar.FirstDate()) {
		return time.Time{}, stockDayError(day, marketcalendar.ErrOutOfCoverage)
	}
	if last := c.calendar.LastDate(); day.After(last) {
		at = time.Date(last.Year(), last.Month(), last.Day(), 23, 59, 0, 0, c.location)
	}
	bucket, ok, err := c.clock.BucketAt(at)
	if err != nil {
		return time.Time{}, c.sessionError(at, err)
	}
	if !ok {
		if bucket, err = c.clock.LatestClosed(at); err != nil {
			return time.Time{}, c.sessionError(at, err)
		}
	}
	return bucket.Start.UTC(), nil
}

// ceil 取不早于 at 的交易时段 bar：早于日历第一根时取第一根。
func (c *stockSessionClock) ceil(at time.Time) (time.Time, error) {
	first, err := c.clock.BucketAtOrdinal(0)
	if err != nil {
		return time.Time{}, c.sessionError(at, err)
	}
	if !at.After(first.Start) {
		return first.Start.UTC(), nil
	}
	bucket, ok, err := c.clock.BucketAt(at)
	if err != nil {
		return time.Time{}, c.sessionError(at, err)
	}
	if ok && bucket.Start.Equal(at) {
		return at.UTC(), nil
	}
	if !ok {
		// 午休、收盘后或休市日：之前最后一根的下一根就是之后的第一根。
		if bucket, err = c.clock.LatestClosed(at); err != nil {
			return time.Time{}, c.sessionError(at, err)
		}
	}
	index, err := c.clock.Ordinal(bucket)
	if err != nil {
		return time.Time{}, c.sessionError(at, err)
	}
	next, err := c.bucketAt(at, index+1)
	if err != nil {
		return time.Time{}, err
	}
	return next.Start.UTC(), nil
}

func (c *stockSessionClock) tradingDayBars() int { return c.perDay }
