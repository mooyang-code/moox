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
type Session struct {
	market barclock.Market

	mu     sync.Mutex
	clocks map[string]barclock.Clock
}

// clock 返回频率对应的 barclock 时钟（按频率缓存：构造时要载入交易日历）。
func (s *Session) clock(freq string) (barclock.Clock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clocks[freq]; ok {
		return c, nil
	}
	duration, err := domain.ParseFrequency(freq)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrUnsupportedFrequency, freq, err)
	}
	c, err := s.market.Clock(duration)
	if err != nil {
		return nil, fmt.Errorf("%w: %s 在 %s 的交易时段上不可用：%v", ErrUnsupportedFrequency, freq, s.market.ID, err)
	}
	if s.clocks == nil {
		s.clocks = map[string]barclock.Clock{}
	}
	s.clocks[freq] = c
	return c, nil
}

// Duration 返回名义周期（5m 就是 5 分钟）。
func (s *Session) Duration(freq string) (time.Duration, error) {
	c, err := s.clock(freq)
	if err != nil {
		return 0, err
	}
	return c.Period(), nil
}

// bucket 返回 data_time 恰为 t 的交易桶。
func (s *Session) bucket(c barclock.Clock, t time.Time, freq string) (barclock.Bucket, error) {
	t = t.UTC()
	items, err := c.Between(t, t.Add(c.Period()), 1)
	if err != nil {
		return barclock.Bucket{}, err
	}
	if len(items) == 0 || !items[0].Key.Equal(t) {
		return barclock.Bucket{}, fmt.Errorf("时间 %s 不是 %s 在 %s 交易时段内的桶起点", t.Format(time.RFC3339), freq, s.market.ID)
	}
	return items[0], nil
}

// Align 要求 t 是交易桶的 data_time。
func (s *Session) Align(t time.Time, freq string) (time.Time, error) {
	c, err := s.clock(freq)
	if err != nil {
		return time.Time{}, err
	}
	b, err := s.bucket(c, t, freq)
	if err != nil {
		return time.Time{}, err
	}
	return b.Key, nil
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
	b, err := s.bucket(c, t, freq)
	if err != nil {
		return nil, err
	}
	window := make([]time.Time, n)
	window[n-1] = b.Key
	for i := n - 2; i >= 0; i-- {
		if b, err = c.Prev(b); err != nil {
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
	b, err := s.bucket(c, t, freq)
	if err != nil {
		return time.Time{}, err
	}
	n, err := c.Ordinal(b)
	if err != nil {
		return time.Time{}, err
	}
	return axisAt(c, n), nil
}

func (s *Session) FromAxis(a time.Time, freq string) (time.Time, error) {
	c, err := s.clock(freq)
	if err != nil {
		return time.Time{}, err
	}
	step := int64(c.Period() / time.Second)
	seconds := a.UTC().Unix()
	if a.Nanosecond() != 0 || seconds%step != 0 {
		return time.Time{}, fmt.Errorf("轴上时间 %d 没有按 %s 对齐", seconds, freq)
	}
	b, err := c.BucketAtOrdinal(seconds / step)
	if err != nil {
		return time.Time{}, err
	}
	return b.Key, nil
}

func (s *Session) AxisEnd(t time.Time, freq string) (time.Time, error) {
	c, err := s.clock(freq)
	if err != nil {
		return time.Time{}, err
	}
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
