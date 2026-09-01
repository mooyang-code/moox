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
)

func ParseFrequency(input string) (Frequency, error) {
	value := strings.ToLower(strings.TrimSpace(input))
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
	default:
		return 0
	}
}
