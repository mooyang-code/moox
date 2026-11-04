package periodclock

import (
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/barclock"
)

// Session 是有交易时段的市场（A 股）的时钟，按 barclock 的交易桶实现：真实时间只有交易桶，午休、隔夜、休市日没有周期；
// 轴上时间 = 纪元 + 交易序号 × 周期。周期必须整除每个交易时段（1d 为一个交易日一根），除不尽的频率报错。
// 换算直接按交易序号计算（barclock 的 Ordinal、BucketAtOrdinal），不建当天的桶列表；热路径经 Axis 调用，不查缓存、不分配内存。
type Session struct {
	market barclock.Market
	// clocks 是各频率的 barclock 时钟（频率 → barclock.Clock），构造时要载入交易日历，所以缓存。
	clocks sync.Map
}

// clock 返回频率对应的 barclock 时钟。
func (s *Session) clock(freq string) (barclock.Clock, error) {
	if c, ok := s.clocks.Load(freq); ok {
		return c.(barclock.Clock), nil
	}
	duration, err := domain.ParseFrequency(freq)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrUnsupportedFrequency, freq, err)
	}
	c, err := s.market.Clock(duration)
	if err != nil {
		return nil, fmt.Errorf("%w: %s 在 %s 的交易时段上不可用：%v", ErrUnsupportedFrequency, freq, s.market.ID, err)
	}
	actual, _ := s.clocks.LoadOrStore(freq, c)
	return actual.(barclock.Clock), nil
}

// Duration 返回名义周期（5m 就是 5 分钟）。
func (s *Session) Duration(freq string) (time.Duration, error) {
	c, err := s.clock(freq)
	if err != nil {
		return 0, err
	}
	return c.Period(), nil
}

// Align 要求 t 是交易桶的 data_time。
func (s *Session) Align(t time.Time, freq string) (time.Time, error) {
	c, err := s.clock(freq)
	if err != nil {
		return time.Time{}, err
	}
	if _, err := c.Ordinal(barclock.Bucket{Key: t}); err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// Window 返回含 t 在内往前 n 根交易桶的 data_time（升序）。
func (s *Session) Window(t time.Time, freq string, n int) ([]time.Time, error) {
	if n <= 0 {
		return nil, fmt.Errorf("window size must be positive")
	}
	c, err := s.clock(freq)
	if err != nil {
		return nil, err
	}
	last, err := c.Ordinal(barclock.Bucket{Key: t})
	if err != nil {
		return nil, err
	}
	window := make([]time.Time, n)
	for i := range window {
		b, err := c.BucketAtOrdinal(last - int64(n-1-i))
		if err != nil {
			return nil, err
		}
		window[i] = b.Key
	}
	return window, nil
}

func (s *Session) ToAxis(t time.Time, freq string) (time.Time, error) {
	c, err := s.clock(freq)
	if err != nil {
		return time.Time{}, err
	}
	return sessionToAxis(c, t)
}

func (s *Session) FromAxis(a time.Time, freq string) (time.Time, error) {
	c, err := s.clock(freq)
	if err != nil {
		return time.Time{}, err
	}
	return sessionFromAxis(c, a)
}

func (s *Session) AxisEnd(t time.Time, freq string) (time.Time, error) {
	c, err := s.clock(freq)
	if err != nil {
		return time.Time{}, err
	}
	return sessionAxisEnd(c, t)
}

func sessionToAxis(c barclock.Clock, t time.Time) (time.Time, error) {
	n, err := c.Ordinal(barclock.Bucket{Key: t})
	if err != nil {
		return time.Time{}, err
	}
	return axisAt(c, n), nil
}

func sessionFromAxis(c barclock.Clock, a time.Time) (time.Time, error) {
	step := int64(c.Period() / time.Second)
	seconds := a.Unix()
	if a.Nanosecond() != 0 || seconds%step != 0 {
		return time.Time{}, fmt.Errorf("轴上时间 %d 没有按 %s 对齐", seconds, c.Period())
	}
	b, err := c.BucketAtOrdinal(seconds / step)
	if err != nil {
		return time.Time{}, err
	}
	return b.Key, nil
}

func sessionAxisEnd(c barclock.Clock, t time.Time) (time.Time, error) {
	b, err := c.LatestClosed(t.UTC())
	if err != nil {
		return time.Time{}, err
	}
	n, err := c.Ordinal(b)
	if err != nil {
		return time.Time{}, err
	}
	return axisAt(c, n+1), nil
}

func axisAt(c barclock.Clock, ordinal int64) time.Time {
	return time.Unix(ordinal*int64(c.Period()/time.Second), 0).UTC()
}
