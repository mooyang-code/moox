// Package periodclock provides calendar-aware period calculations for factor jobs.
package periodclock

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrUnsupportedCalendar  = errors.New("unsupported space calendar")
	ErrUnsupportedFrequency = errors.New("unsupported period frequency")
)

// Clock calculates period boundaries and lookback windows for a market calendar.
type Clock interface {
	Duration(freq string) (time.Duration, error)
	Align(t time.Time, freq string) (time.Time, error)
	Window(t time.Time, freq string, n int) ([]time.Time, error)
}

// ForSpace returns the period clock used by the given market space.
func ForSpace(spaceID string) (Clock, error) {
	if spaceID != "crypto" {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCalendar, spaceID)
	}
	return Continuous{}, nil
}
