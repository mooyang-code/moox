package domain

import (
	"fmt"
	"strconv"
	"time"
)

// ParseFrequency parses the compact frequency identity accepted by Storage.
func ParseFrequency(value string) (time.Duration, error) {
	if len(value) < 2 {
		return 0, fmt.Errorf("frequency must be a positive duration")
	}
	count, err := strconv.ParseUint(value[:len(value)-1], 10, 64)
	if err != nil || count == 0 {
		return 0, fmt.Errorf("frequency must be a positive duration")
	}
	var unit time.Duration
	switch value[len(value)-1] {
	case 'm':
		unit = time.Minute
	case 'h', 'H':
		unit = time.Hour
	case 'd', 'D':
		unit = 24 * time.Hour
	case 'w', 'W':
		unit = 7 * 24 * time.Hour
	case 'M':
		unit = 30 * 24 * time.Hour
	case 'y', 'Y':
		unit = 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("frequency must be a positive duration")
	}
	if count > uint64((1<<63-1)/unit) {
		return 0, fmt.Errorf("frequency must be a positive duration")
	}
	return time.Duration(count) * unit, nil
}

// NextPeriod advances a Storage period using calendar semantics where needed.
// Month/year periods cannot be represented by a fixed duration without drift.
func NextPeriod(at time.Time, value string) (time.Time, error) {
	if len(value) < 2 {
		return time.Time{}, fmt.Errorf("frequency must be a positive duration")
	}
	count, err := strconv.Atoi(value[:len(value)-1])
	if err != nil || count <= 0 {
		return time.Time{}, fmt.Errorf("frequency must be a positive duration")
	}
	switch value[len(value)-1] {
	case 'm':
		return at.Add(time.Duration(count) * time.Minute), nil
	case 'h', 'H':
		return at.Add(time.Duration(count) * time.Hour), nil
	case 'd', 'D':
		return at.AddDate(0, 0, count), nil
	case 'w', 'W':
		return at.AddDate(0, 0, 7*count), nil
	case 'M':
		return at.AddDate(0, count, 0), nil
	case 'y', 'Y':
		return at.AddDate(count, 0, 0), nil
	default:
		return time.Time{}, fmt.Errorf("frequency must be a positive duration")
	}
}
