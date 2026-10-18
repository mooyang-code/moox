package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
)

const day = 24 * time.Hour
const monthScheduleInterval = 31 * day

// ParseScheduleInterval parses a UTC scheduling period. Calendar months use a
// conservative 31-day interval for freshness policy; ScheduleDecision keeps
// their actual month-boundary execution semantics.
func ParseScheduleInterval(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if parsed, err := frequency.Parse(raw); err == nil && parsed == frequency.Month1 {
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
