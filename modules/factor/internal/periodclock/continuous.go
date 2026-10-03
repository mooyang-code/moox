package periodclock

import (
	"fmt"
	"math"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

// Continuous uses fixed-duration periods anchored to the Unix epoch.
type Continuous struct{}

func (Continuous) Duration(freq string) (time.Duration, error) {
	switch freq {
	case "1m", "5m", "15m", "30m", "1h", "4h", "1d":
	default:
		return 0, fmt.Errorf("%w: %q", ErrUnsupportedFrequency, freq)
	}

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
