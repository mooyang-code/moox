package marketdata

import (
	"fmt"
	"strings"
	"time"
)

type Frequency string

const (
	FrequencyMinute Frequency = "1m"
	Frequency5Min   Frequency = "5m"
	Frequency15Min  Frequency = "15m"
	Frequency30Min  Frequency = "30m"
	FrequencyHour   Frequency = "1h"
	FrequencyDay    Frequency = "1d"
	FrequencyWeek   Frequency = "1w"
	FrequencyMonth  Frequency = "1M"
)

func ParseFrequency(input string) (Frequency, error) {
	raw := strings.TrimSpace(input)
	if raw == string(FrequencyMonth) {
		return FrequencyMonth, nil
	}
	value := strings.ToLower(raw)
	if value == "60m" {
		value = string(FrequencyHour)
	}
	switch Frequency(value) {
	case FrequencyMinute, Frequency5Min, Frequency15Min, Frequency30Min, FrequencyHour, FrequencyDay, FrequencyWeek:
		return Frequency(value), nil
	default:
		return "", fmt.Errorf("unsupported frequency %q", input)
	}
}

func (f Frequency) Duration() time.Duration {
	switch f {
	case FrequencyMinute:
		return time.Minute
	case Frequency5Min:
		return 5 * time.Minute
	case Frequency15Min:
		return 15 * time.Minute
	case Frequency30Min:
		return 30 * time.Minute
	case FrequencyHour:
		return time.Hour
	case FrequencyDay:
		return 24 * time.Hour
	case FrequencyWeek:
		return 7 * 24 * time.Hour
	case FrequencyMonth:
		// A month has no fixed duration. Callers that need a closed boundary
		// must use BarEnd, which applies calendar arithmetic.
		return 0
	default:
		return 0
	}
}

func (f Frequency) BarEnd(start time.Time) time.Time {
	if f == FrequencyMonth {
		return start.AddDate(0, 1, 0)
	}
	return start.Add(f.Duration())
}
