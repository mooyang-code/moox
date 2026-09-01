package marketdata

import (
	"fmt"
	"strings"
	"time"
)

// TimestampMode describes what a provider's timestamp labels. Providers must
// declare this before their labels are converted into the canonical bar key.
type TimestampMode string

const (
	TimestampStartLabel TimestampMode = "start-label"
	TimestampEndLabel   TimestampMode = "end-label"
)

// BarDefinition owns the time semantics shared by a provider and a market.
// Returned times are UTC even though period arithmetic happens in Location.
type BarDefinition struct {
	Frequency     string
	Location      *time.Location
	TimestampMode TimestampMode
}

func (definition BarDefinition) Validate() error {
	if strings.TrimSpace(definition.Frequency) == "" {
		return fmt.Errorf("bar frequency is required")
	}
	if definition.Location == nil {
		return fmt.Errorf("bar timezone is required")
	}
	if definition.TimestampMode != TimestampStartLabel && definition.TimestampMode != TimestampEndLabel {
		return fmt.Errorf("timestamp mode %q is invalid", definition.TimestampMode)
	}
	if _, ok := periodForFrequency(definition.Frequency); !ok {
		return fmt.Errorf("bar frequency %q is unsupported", definition.Frequency)
	}
	return nil
}

// NormalizeLabel converts a provider label to the canonical [start, end)
// interval. End-labelled providers are shifted back by exactly one period.
func (definition BarDefinition) NormalizeLabel(label time.Time) (time.Time, time.Time, error) {
	if err := definition.Validate(); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if label.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("bar timestamp is required")
	}
	local := label.In(definition.Location)
	var start, end time.Time
	switch definition.TimestampMode {
	case TimestampStartLabel:
		start = local
		end = shiftBarPeriod(local, definition.Frequency, 1)
	case TimestampEndLabel:
		end = local
		start = shiftBarPeriod(local, definition.Frequency, -1)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("bar interval is not positive")
	}
	return start.UTC(), end.UTC(), nil
}

func periodForFrequency(frequency string) (int, bool) {
	switch strings.TrimSpace(frequency) {
	case "1m":
		return 1, true
	case "5m":
		return 5, true
	case "15m":
		return 15, true
	case "30m":
		return 30, true
	case "60m", "1h":
		return 60, true
	case "1d":
		return 0, true
	case "1w":
		return 0, true
	case "1M", "1mo", "1mth":
		return 0, true
	case "1q":
		return 0, true
	case "1y":
		return 0, true
	default:
		return 0, false
	}
}

func shiftBarPeriod(value time.Time, frequency string, direction int) time.Time {
	frequency = strings.TrimSpace(frequency)
	switch frequency {
	case "1m", "5m", "15m", "30m", "60m", "1h":
		minutes, _ := periodForFrequency(frequency)
		return value.Add(time.Duration(direction*minutes) * time.Minute)
	case "1d":
		return value.AddDate(0, 0, direction)
	case "1w":
		return value.AddDate(0, 0, direction*7)
	case "1M", "1mo", "1mth":
		return shiftCalendarMonths(value, direction)
	case "1q":
		return shiftCalendarMonths(value, direction*3)
	case "1y":
		return value.AddDate(direction, 0, 0)
	default:
		return value
	}
}

// shiftCalendarMonths clamps the day instead of allowing time.Date/AddDate to
// normalize February 31 into a date in March.
func shiftCalendarMonths(value time.Time, months int) time.Time {
	year, month, day := value.Date()
	target := time.Date(year, month, 1, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location()).AddDate(0, months, 0)
	lastDay := time.Date(target.Year(), target.Month()+1, 0, 0, 0, 0, 0, target.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(target.Year(), target.Month(), day, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
}
