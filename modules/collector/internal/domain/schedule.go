package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const day = 24 * time.Hour
const monthScheduleInterval = 31 * day

// ParseScheduleInterval parses a UTC scheduling period. Calendar months use a
// conservative 31-day interval for freshness policy; ScheduleDecision keeps
// their actual month-boundary execution semantics.
func ParseScheduleInterval(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "1M" {
		return monthScheduleInterval, nil
	}
	raw = strings.ToLower(raw)
	var (
		interval time.Duration
		err      error
	)
	if strings.HasSuffix(raw, "d") || strings.HasSuffix(raw, "w") {
		value := raw[:len(raw)-1]
		if !isPositiveInteger(value) {
			return 0, fmt.Errorf("schedule interval must be a positive duration")
		}
		count, parseErr := strconv.ParseInt(value, 10, 64)
		unit := day
		if strings.HasSuffix(raw, "w") {
			unit = 7 * day
		}
		if parseErr != nil || count > int64(^uint64(0)>>1)/int64(unit) {
			return 0, fmt.Errorf("schedule interval must be a positive duration")
		}
		interval = time.Duration(count) * unit
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
	raw = strings.TrimSpace(raw)
	interval, err := ParseScheduleInterval(raw)
	if err != nil {
		return time.Time{}, false, err
	}
	candidate := now.UTC().Truncate(time.Minute).Add(time.Minute)
	if raw == "1M" {
		if candidate.Day() != 1 || candidate.Hour() != 0 || candidate.Minute() != 0 {
			return time.Time{}, false, nil
		}
		return candidate, true, nil
	}
	if strings.HasSuffix(strings.ToLower(raw), "w") {
		if candidate.Weekday() != time.Monday || candidate.Hour() != 0 || candidate.Minute() != 0 {
			return time.Time{}, false, nil
		}
		weeks, parseErr := strconv.Atoi(raw[:len(raw)-1])
		if parseErr != nil || weeks <= 0 {
			return time.Time{}, false, fmt.Errorf("schedule interval must be a positive duration")
		}
		anchor := time.Date(1970, time.January, 5, 0, 0, 0, 0, time.UTC)
		if int(candidate.Sub(anchor)/(7*day))%weeks != 0 {
			return time.Time{}, false, nil
		}
		return candidate, true, nil
	}
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
