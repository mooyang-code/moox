package periodclock

import (
	"fmt"
	"math"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

// Continuous 是锚定 Unix 纪元的定长周期，用于 7×24 市场的真实时间，也用于引擎内部所有空间的轴上时间。
// 它的轴换算都是恒等的。
type Continuous struct{}

func (Continuous) Duration(freq string) (time.Duration, error) {
	duration, err := domain.ParseFrequency(freq)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrUnsupportedFrequency, freq, err)
	}
	return duration, nil
}

func (c Continuous) Align(t time.Time, freq string) (time.Time, error) {
	duration, err := c.Duration(freq)
	if err != nil {
		return time.Time{}, err
	}
	utc := t.UTC()
	periodSeconds := int64(duration / time.Second)
	if utc.Nanosecond() != 0 || utc.Unix()%periodSeconds != 0 {
		return time.Time{}, fmt.Errorf("time %s is not aligned to %s", utc.Format(time.RFC3339Nano), freq)
	}
	return utc, nil
}

func (c Continuous) Window(t time.Time, freq string, n int) ([]time.Time, error) {
	aligned, err := c.Align(t, freq)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, fmt.Errorf("window size must be positive")
	}
	duration, err := c.Duration(freq)
	if err != nil {
		return nil, err
	}
	periods := n - 1
	if uint64(periods) > uint64(math.MaxInt64/int64(duration)) {
		return nil, fmt.Errorf("window duration overflows time.Duration")
	}

	window := make([]time.Time, n)
	for i := range window {
		window[i] = aligned.Add(-time.Duration(n-1-i) * duration)
	}
	return window, nil
}

func (c Continuous) ToAxis(t time.Time, freq string) (time.Time, error) { return c.Align(t, freq) }

func (c Continuous) FromAxis(a time.Time, freq string) (time.Time, error) { return c.Align(a, freq) }

// AxisEnd 返回 t 向下对齐到周期边界的时刻（收盘不晚于 t 的最新一根的起点 + 周期）。
func (c Continuous) AxisEnd(t time.Time, freq string) (time.Time, error) {
	duration, err := c.Duration(freq)
	if err != nil {
		return time.Time{}, err
	}
	step := int64(duration / time.Second)
	seconds := t.UTC().Unix()
	rem := seconds % step
	if rem < 0 {
		rem += step
	}
	return time.Unix(seconds-rem, 0).UTC(), nil
}
