package barclock

import (
	"fmt"
	"strings"
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
	return &sessionClock{loc: location, days: days, spans: spans, period: period}, nil
}

func formatClock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

func (c *sessionClock) Alignment() string      { return AlignmentSession }
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

// dayBuckets 返回交易日 date 的全部桶；非交易日由调用方先排除。
func (c *sessionClock) dayBuckets(date marketcalendar.CivilDate) []Bucket {
	if c.period == 24*time.Hour {
		key := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
		return []Bucket{{Key: key, Start: c.at(date, c.spans[0].open), End: c.at(date, c.spans[len(c.spans)-1].close)}}
	}
	var out []Bucket
	for _, item := range c.spans {
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
	for _, item := range c.spans {
		for offset := item.open; offset+source <= item.close; offset += source {
			at := c.at(date, offset)
			if !at.Before(b.Start) && at.Before(b.End) {
				out = append(out, at)
			}
		}
	}
	return out, nil
}
