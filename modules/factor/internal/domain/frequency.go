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
