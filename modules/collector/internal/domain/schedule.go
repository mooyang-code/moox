package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const day = 24 * time.Hour

// ParseScheduleInterval parses a fixed UTC scheduling period.
func ParseScheduleInterval(raw string) (time.Duration, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var (
		interval time.Duration
		err      error
	)
	if strings.HasSuffix(raw, "d") {
		daysRaw := strings.TrimSuffix(raw, "d")
		if !isPositiveInteger(daysRaw) {
			return 0, fmt.Errorf("schedule interval must be a positive duration")
		}
		days, parseErr := strconv.ParseInt(daysRaw, 10, 64)
		if parseErr != nil || days > int64(^uint64(0)>>1)/int64(day) {
			return 0, fmt.Errorf("schedule interval must be a positive duration")
		}
		interval = time.Duration(days) * day
	} else {
		interval, err = time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			return 0, fmt.Errorf("schedule interval must be a positive duration")
		}
	}
	if interval < time.Minute || interval%time.Minute != 0 {
		return 0, fmt.Errorf("schedule interval must be at least 1 minute and use whole minutes")
	}
	return interval, nil
}

// ScheduleDecision returns the next UTC minute only when it aligns with the period.
func ScheduleDecision(now time.Time, raw string) (time.Time, bool, error) {
	interval, err := ParseScheduleInterval(raw)
	if err != nil {
		return time.Time{}, false, err
	}
	candidate := now.UTC().Truncate(time.Minute).Add(time.Minute)
	if candidate.UnixNano()%interval.Nanoseconds() != 0 {
		return time.Time{}, false, nil
	}
	return candidate, true, nil
}

func isPositiveInteger(raw string) bool {
	if raw == "" {
		return false
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return false
		}
	}
	return raw != "0"
}
