package period

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// stockSessionClock 是股票市场的日内周期：桶由 barclock 按交易时段切分（不跨午休），bar_start 是真实的开盘时刻（UTC），
// bar_end 是 bar_start + 周期；BarIndex 是 barclock 的交易序号，午休、隔夜、休市不占根数，半日市收市之后也没有根，
// 与因子的交易序号轴一致。
type stockSessionClock struct {
	market    *sessionMarket
	clock     barclock.Clock
	frequency string
	period    time.Duration
	calendar  marketcalendar.TradingCalendar
	location  *time.Location
	// perDay 是完整交易日的根数，只用于估算；半日市当天更少，精确换算用 dayBars。
	perDay int
}

// sessionClockKey 是日内周期时钟的缓存键：日历加周期，各市场互不串用。
type sessionClockKey struct{ calendarID, frequency string }

// stockSessionClocks 缓存各市场各日内周期的时钟（sessionClockKey → *stockSessionClock）：时钟不可变，构造时要扫一遍交易时段。
var stockSessionClocks sync.Map

// stockSessionClockFor 返回市场某个日内周期的时钟；周期不在 barclock 允许的日内周期之内时返回 ErrUnsupportedCalendar。
func stockSessionClockFor(market *sessionMarket, frequency string) (periodClock, error) {
	key := sessionClockKey{calendarID: market.calendarID, frequency: frequency}
	if cached, ok := stockSessionClocks.Load(key); ok {
		return cached.(*stockSessionClock), nil
	}
	if !slices.Contains(market.market.AllowedIntraday(), frequency) {
		return nil, fmt.Errorf("%w：日历 %q 不支持频率 %q（日内周期须整除每个交易时段：%v）", ErrUnsupportedCalendar, market.calendarID, frequency, market.market.Sessions)
	}
	period, err := ParseBarDuration(frequency)
	if err != nil {
		return nil, err
	}
	clock, err := barclock.NewSession(market.calendar, market.location, market.market.Sessions, period)
	if err != nil {
		return nil, fmt.Errorf("%s %s 的交易时段时钟无法构造：%w", market.name(), frequency, err)
	}
	perDay := market.market.FullDayBuckets(period)
	if perDay <= 0 {
		return nil, fmt.Errorf("%s %s 每个交易日的根数无法计算", market.name(), frequency)
	}
	value := &stockSessionClock{market: market, clock: clock, frequency: frequency, period: period, calendar: market.calendar, location: market.location, perDay: perDay}
	actual, _ := stockSessionClocks.LoadOrStore(key, value)
	return actual.(*stockSessionClock), nil
}

// dayBars 返回第 index 个交易日（日历首个交易日为 0）第一根与最后一根的交易序号：半日市当天的根数比完整交易日少，
// 不能按“交易日序号 × 每日根数”折算。序号越过日历首尾时 ok 为假。
func (c *stockSessionClock) dayBars(index int64) (first, last int64, ok bool) {
	if index < 0 {
		return 0, 0, false
	}
	day, err := c.calendar.TradingDayAt(int(index))
	if err != nil {
		return 0, 0, false
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, c.location)
	buckets, err := c.clock.Between(start, start.Add(24*time.Hour), 24*60)
	if err != nil || len(buckets) == 0 {
		return 0, 0, false
	}
	if first, err = c.clock.Ordinal(buckets[0]); err != nil {
		return 0, 0, false
	}
	return first, first + int64(len(buckets)) - 1, true
}

// stockIntraday 报告日历与周期是否是受支持的股票市场日内周期。
func StockIntraday(calendarID, frequency string) bool {
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
	translated := translateCalendarError(c.market, err)
	return &describedError{message: fmt.Sprintf("%s %s 周期 %s：%s", c.market.name(), c.frequency, c.market.label(at), translated.Error()), cause: err}
}

// shiftError 说明从 at 移动到交易序号 target 时越过了内嵌日历的首尾：与日线一致，用“之前 / 之后没有交易日”的哨兵错误，
// 调用方据此区分“需要追溯到日历起点之前”与其他失败。
func (c *stockSessionClock) shiftError(at time.Time, target int64) error {
	sentinel := marketcalendar.ErrNoNextTradingDay
	if target < 0 {
		sentinel = marketcalendar.ErrNoPreviousTradingDay
	}
	return &describedError{message: fmt.Sprintf("%s %s 周期 %s：%s", c.market.name(), c.frequency, c.market.label(at), translateCalendarError(c.market, sentinel).Error()), cause: sentinel}
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
		return PeriodBoundaries{}, fmt.Errorf("Storage 周期 %s 不是%s交易时段内 %s bar 的起点", c.market.label(start), c.market.spacedName(), c.frequency)
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
		return PeriodBoundaries{}, fmt.Errorf("bar_end %s 不是%s交易时段内 %s bar 的终点", c.market.label(end), c.market.spacedName(), c.frequency)
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
		return time.Time{}, stockDayError(c.market, day, marketcalendar.ErrOutOfCoverage)
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
