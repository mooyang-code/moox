package barclock

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// maxWalkDays 是沿交易日历前后查找的最大天数：足以跨过最长的长假（春节约 9 天）。
const maxWalkDays = 40

// maxBetweenDays 限制 Between 一次最多遍历的自然日数。
const maxBetweenDays = 3660

// TradingDays 判断某天是否交易日；覆盖范围外返回 OutOfCoverage 与错误。marketcalendar.TradingCalendar 实现它。
type TradingDays interface {
	Status(marketcalendar.CivilDate) (marketcalendar.CoverageStatus, error)
}

// IndexedTradingDays 在 TradingDays 之外还能按序号定位交易日，交易序号（Ordinal）需要它。
// marketcalendar.TradingCalendar 实现它。
type IndexedTradingDays interface {
	TradingDays
	TradingDayIndex(marketcalendar.CivilDate) (int, error)
	TradingDayAt(int) (marketcalendar.CivilDate, error)
}

// EarlyCloses 是交易日历的可选能力：提前收市日（半日市）当天收市时刻之后没有桶。
// 交易日历实现了它才生效，没实现的按每个交易日时段相同处理。marketcalendar.TradingCalendar 实现它。
type EarlyCloses interface {
	// EarlyClose 返回提前收市日当天的收市时刻（距本地零点的时长）；不是提前收市日时 ok 为假。
	EarlyClose(marketcalendar.CivilDate) (closeAt time.Duration, ok bool)
	// EarlyCloseDates 返回全部提前收市日，按日期升序。
	EarlyCloseDates() []marketcalendar.CivilDate
}

// span 是一个交易时段，用距本地零点的时长表示。
type span struct{ open, close time.Duration }

func (s span) length() time.Duration { return s.close - s.open }

// parseSpans 解析形如 "09:30-11:30" 的交易时段，要求按开盘先后排列且互不重叠。
func parseSpans(sessions []string) ([]span, error) {
	if len(sessions) == 0 {
		return nil, fmt.Errorf("%w：没有交易时段", ErrNoCalendar)
	}
	spans := make([]span, 0, len(sessions))
	for _, raw := range sessions {
		open, close, ok := strings.Cut(strings.TrimSpace(raw), "-")
		start, startErr := clockOfDay(open)
		end, endErr := clockOfDay(close)
		if !ok || startErr != nil || endErr != nil || end <= start {
			return nil, fmt.Errorf("交易时段 %q 无效", raw)
		}
		if len(spans) > 0 && start < spans[len(spans)-1].close {
			return nil, fmt.Errorf("交易时段 %q 与前一个时段重叠或顺序不对", raw)
		}
		spans = append(spans, span{open: start, close: end})
	}
	return spans, nil
}

// spansOn 返回交易日 date 当天实际的交易时段：提前收市日只到收市时刻为止，其余日子就是 spans 本身（不分配内存）。
func spansOn(early EarlyCloses, spans []span, date marketcalendar.CivilDate) []span {
	if early == nil {
		return spans
	}
	closeAt, ok := early.EarlyClose(date)
	if !ok {
		return spans
	}
	out := make([]span, 0, len(spans))
	for _, item := range spans {
		if item.open >= closeAt {
			break
		}
		if item.close > closeAt {
			item.close = closeAt
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		// 收市时刻早于开盘，是日历数据的错误：按普通交易日处理，不让当天变成没有时段。
		return spans
	}
	return out
}

func clockOfDay(value string) (time.Duration, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return time.Duration(parsed.Hour())*time.Hour + time.Duration(parsed.Minute())*time.Minute, nil
}

type sessionClock struct {
	loc    *time.Location
	days   TradingDays
	spans  []span
	period time.Duration
	// perDay 是普通交易日的桶数；before[i] 是第 i 个交易时段之前的桶数。
	// 交易序号直接按它们计算，热路径不建当天的桶列表。
	perDay int64
	before []int64
	// early 是交易日历的提前收市日；日历没有这项能力时为 nil。
	early EarlyCloses
	// short 是提前收市日的累计序号表，第一次算交易序号时才建（Monitor 每次都新建时钟，但从不算交易序号）。
	shortOnce sync.Once
	short     []shortDay
}

// shortDay 是一个提前收市日在交易序号里的位置。普通交易日每天 perDay 根，所以累计序号表只需要记提前收市日：
// 没有提前收市日的日历（A 股）这张表是空的，交易序号就是“交易日序号 × 每日根数 + 日内序号”。
type shortDay struct {
	// day 是这一天的交易日序号。
	day int64
	// missing 是到这一天为止（含）比“每天 perDay 根”累计少掉的根数。
	missing int64
	// next 是下一个交易日第一根的交易序号。
	next int64
}

// NewSession 返回按交易时段切分的时钟。日内周期必须整除每个交易时段的长度；1d 一个交易日一根。
func NewSession(days TradingDays, location *time.Location, sessions []string, period time.Duration) (Clock, error) {
	if days == nil || location == nil {
		return nil, ErrNoCalendar
	}
	spans, err := parseSpans(sessions)
	if err != nil {
		return nil, err
	}
	if period < time.Minute || period > 24*time.Hour {
		return nil, fmt.Errorf("session 的周期必须在 1m～1d")
	}
	if period < 24*time.Hour {
		for _, item := range spans {
			if item.length()%period != 0 {
				return nil, fmt.Errorf("%w：周期 %s 不能整除交易时段 %s–%s（%d 分钟）", ErrPeriodNotDivisible, period, formatClock(item.open), formatClock(item.close), int(item.length()/time.Minute))
			}
		}
	}
	c := &sessionClock{loc: location, days: days, spans: spans, period: period, perDay: 1, before: make([]int64, len(spans))}
	c.early, _ = days.(EarlyCloses)
	if period < 24*time.Hour {
		c.perDay = 0
		for i, item := range spans {
			c.before[i] = c.perDay
			c.perDay += int64(item.length() / period)
		}
	}
	return c, nil
}

func formatClock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

func (c *sessionClock) Alignment() string     { return AlignmentSession }
func (c *sessionClock) Period() time.Duration { return c.period }

func (c *sessionClock) dateOf(t time.Time) marketcalendar.CivilDate {
	local := t.In(c.loc)
	date, _ := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
	return date
}

func (c *sessionClock) shift(date marketcalendar.CivilDate, days int) marketcalendar.CivilDate {
	moved := time.Date(date.Year(), date.Month(), date.Day()+days, 12, 0, 0, 0, c.loc)
	next, _ := marketcalendar.NewCivilDate(moved.Year(), moved.Month(), moved.Day())
	return next
}

func (c *sessionClock) at(date marketcalendar.CivilDate, offset time.Duration) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), int(offset/time.Hour), int(offset%time.Hour/time.Minute), 0, 0, c.loc).UTC()
}

func (c *sessionClock) isTradingDay(date marketcalendar.CivilDate) (bool, error) {
	status, err := c.days.Status(date)
	if err != nil {
		return false, fmt.Errorf("%w：%s：%v", ErrOutOfCoverage, date, err)
	}
	return status == marketcalendar.TradingDay, nil
}

// daily 返回交易日 date 的 1d 桶：首个时段开盘到当天实际收市。
func (c *sessionClock) daily(date marketcalendar.CivilDate) Bucket {
	spans := spansOn(c.early, c.spans, date)
	key := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	return Bucket{Key: key, Start: c.at(date, spans[0].open), End: c.at(date, spans[len(spans)-1].close)}
}

// dayBuckets 返回交易日 date 的全部桶，按当天实际的交易时段切分（提前收市日收市之后没有桶）；非交易日由调用方先排除。
func (c *sessionClock) dayBuckets(date marketcalendar.CivilDate) []Bucket {
	if c.period == 24*time.Hour {
		return []Bucket{c.daily(date)}
	}
	var out []Bucket
	for _, item := range spansOn(c.early, c.spans, date) {
		for offset := item.open; offset+c.period <= item.close; offset += c.period {
			start := c.at(date, offset)
			out = append(out, Bucket{Key: start, Start: start, End: start.Add(c.period)})
		}
	}
	return out
}

func (c *sessionClock) BucketAt(t time.Time) (Bucket, bool, error) {
	date := c.dateOf(t)
	trading, err := c.isTradingDay(date)
	if err != nil || !trading {
		return Bucket{}, false, err
	}
	for _, b := range c.dayBuckets(date) {
		if !t.Before(b.Start) && t.Before(b.End) {
			return b, true, nil
		}
	}
	return Bucket{}, false, nil
}

func (c *sessionClock) LatestClosed(now time.Time) (Bucket, error) {
	date := c.dateOf(now)
	for i := 0; i <= maxWalkDays; i, date = i+1, c.shift(date, -1) {
		trading, err := c.isTradingDay(date)
		if err != nil {
			return Bucket{}, err
		}
		if !trading {
			continue
		}
		list := c.dayBuckets(date)
		for j := len(list) - 1; j >= 0; j-- {
			if !list[j].End.After(now) {
				return list[j], nil
			}
		}
	}
	return Bucket{}, ErrNoBucket
}

func (c *sessionClock) ClosedBefore(now time.Time, n int) ([]Bucket, error) {
	return closedBefore(c, now, n)
}

// neighbor 返回 b 之后（step=1）或之前（step=-1）的桶。
func (c *sessionClock) neighbor(b Bucket, step int) (Bucket, error) {
	date := c.dateOf(b.Start)
	list := c.dayBuckets(date)
	for i, item := range list {
		if item.Start.Equal(b.Start) {
			if j := i + step; j >= 0 && j < len(list) {
				return list[j], nil
			}
			break
		}
	}
	for i := 1; i <= maxWalkDays; i++ {
		next := c.shift(date, step*i)
		trading, err := c.isTradingDay(next)
		if err != nil {
			return Bucket{}, err
		}
		if !trading {
			continue
		}
		items := c.dayBuckets(next)
		if step > 0 {
			return items[0], nil
		}
		return items[len(items)-1], nil
	}
	return Bucket{}, ErrNoBucket
}

func (c *sessionClock) Next(b Bucket) (Bucket, error) { return c.neighbor(b, 1) }
func (c *sessionClock) Prev(b Bucket) (Bucket, error) { return c.neighbor(b, -1) }

func (c *sessionClock) Between(start, end time.Time, limit int) ([]Bucket, error) {
	var out []Bucket
	date := c.dateOf(start)
	last := c.dateOf(end)
	for i := 0; i < maxBetweenDays && len(out) < limit; i, date = i+1, c.shift(date, 1) {
		if date.After(last) {
			break
		}
		trading, err := c.isTradingDay(date)
		if err != nil {
			return out, err
		}
		if !trading {
			continue
		}
		for _, b := range c.dayBuckets(date) {
			if b.End.After(start) && b.Start.Before(end) {
				out = append(out, b)
				if len(out) >= limit {
					break
				}
			}
		}
	}
	return out, nil
}

func (c *sessionClock) indexed() (IndexedTradingDays, error) {
	indexed, ok := c.days.(IndexedTradingDays)
	if !ok {
		return nil, fmt.Errorf("%w：交易日历不支持按序号定位", ErrNoCalendar)
	}
	return indexed, nil
}

// shortDays 返回提前收市日的累计序号表，第一次调用时建。1d 一天一根，提前收市日不少根，表是空的。
func (c *sessionClock) shortDays(indexed IndexedTradingDays) []shortDay {
	c.shortOnce.Do(func() {
		if c.early == nil || c.period == 24*time.Hour {
			return
		}
		var missing int64
		for _, date := range c.early.EarlyCloseDates() {
			day, err := indexed.TradingDayIndex(date)
			if err != nil {
				continue
			}
			var count int64
			for _, item := range spansOn(c.early, c.spans, date) {
				count += int64(item.length() / c.period)
			}
			if count >= c.perDay {
				continue
			}
			missing += c.perDay - count
			c.short = append(c.short, shortDay{day: int64(day), missing: missing, next: (int64(day)+1)*c.perDay - missing})
		}
	})
	return c.short
}

// Ordinal 按桶的 Key 定位（日内桶的 Key 是开盘时刻，1d 桶的 Key 是交易日期的 UTC 零点），直接计算：
// 交易日序号 × 每日根数 + 所在时段之前的根数 + (本地时刻 − 时段开盘) / 周期 − 此前各提前收市日少掉的根数。
// 表建好后不分配内存（出错时除外）。
func (c *sessionClock) Ordinal(b Bucket) (int64, error) {
	indexed, err := c.indexed()
	if err != nil {
		return 0, err
	}
	var (
		date  marketcalendar.CivilDate
		intra int64
	)
	if c.period == 24*time.Hour {
		key := b.Key.UTC()
		if key.Hour() != 0 || key.Minute() != 0 || key.Second() != 0 || key.Nanosecond() != 0 {
			return 0, c.notBucket(b.Key)
		}
		date, _ = marketcalendar.NewCivilDate(key.Year(), key.Month(), key.Day())
	} else {
		local := b.Key.In(c.loc)
		date, _ = marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
		offset := time.Duration(local.Hour())*time.Hour + time.Duration(local.Minute())*time.Minute +
			time.Duration(local.Second())*time.Second + time.Duration(local.Nanosecond())
		found := false
		for i, item := range c.spans {
			if offset >= item.open && offset < item.close {
				if rel := offset - item.open; rel%c.period == 0 {
					intra, found = c.before[i]+int64(rel/c.period), true
				}
				break
			}
		}
		if !found {
			return 0, c.notBucket(b.Key)
		}
		if c.early != nil {
			if closeAt, ok := c.early.EarlyClose(date); ok && offset+c.period > closeAt {
				return 0, c.notBucket(b.Key)
			}
		}
	}
	day, err := indexed.TradingDayIndex(date)
	if errors.Is(err, marketcalendar.ErrNotTradingDay) {
		return 0, c.notBucket(b.Key)
	}
	if err != nil {
		return 0, fmt.Errorf("%w：%s：%v", ErrOutOfCoverage, date, err)
	}
	ordinal := int64(day)*c.perDay + intra
	short := c.shortDays(indexed)
	for i := len(short) - 1; i >= 0; i-- {
		if short[i].day < int64(day) {
			ordinal -= short[i].missing
			break
		}
	}
	return ordinal, nil
}

func (c *sessionClock) notBucket(t time.Time) error {
	return fmt.Errorf("%w：%s 不是 %s 的交易桶", ErrNoBucket, t.UTC().Format(time.RFC3339), c.period)
}

// BucketAtOrdinal 是 Ordinal 的反向，同样直接计算，表建好后不分配内存（1d 的提前收市日除外）。
func (c *sessionClock) BucketAtOrdinal(n int64) (Bucket, error) {
	indexed, err := c.indexed()
	if err != nil {
		return Bucket{}, err
	}
	if n < 0 {
		return Bucket{}, fmt.Errorf("%w：交易序号 %d 为负", ErrOutOfCoverage, n)
	}
	// 把序号换成“每天 perDay 根”下的序号：补回此前各提前收市日少掉的根数。
	short := c.shortDays(indexed)
	for i := len(short) - 1; i >= 0; i-- {
		if n >= short[i].next {
			n += short[i].missing
			break
		}
	}
	date, err := indexed.TradingDayAt(int(n / c.perDay))
	if err != nil {
		return Bucket{}, fmt.Errorf("%w：%v", ErrOutOfCoverage, err)
	}
	if c.period == 24*time.Hour {
		return c.daily(date), nil
	}
	intra := n % c.perDay
	i := len(c.spans) - 1
	for i > 0 && c.before[i] > intra {
		i--
	}
	start := c.at(date, c.spans[i].open+time.Duration(intra-c.before[i])*c.period)
	return Bucket{Key: start, Start: start, End: start.Add(c.period)}, nil
}

func (c *sessionClock) ValidateSource(source time.Duration) error {
	if source <= 0 || source > c.period {
		return ErrPeriodNotDivisible
	}
	if c.period < 24*time.Hour && c.period%source != 0 {
		return ErrPeriodNotDivisible
	}
	for _, item := range c.spans {
		if item.length()%source != 0 {
			return fmt.Errorf("%w：源周期 %s 不能整除交易时段 %s–%s（%d 分钟）", ErrPeriodNotDivisible, source, formatClock(item.open), formatClock(item.close), int(item.length()/time.Minute))
		}
	}
	return nil
}

func (c *sessionClock) SourceTimes(b Bucket, source time.Duration) ([]time.Time, error) {
	if err := c.ValidateSource(source); err != nil {
		return nil, err
	}
	date := c.dateOf(b.Start)
	var out []time.Time
	for _, item := range spansOn(c.early, c.spans, date) {
		for offset := item.open; offset+source <= item.close; offset += source {
			at := c.at(date, offset)
			if !at.Before(b.Start) && at.Before(b.End) {
				out = append(out, at)
			}
		}
	}
	return out, nil
}
