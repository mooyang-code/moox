package marketfetch

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/report"
)

// periodWindows returns the currently forming Storage period and the next
// period. We intentionally do not seed older closed periods on process start:
// a fresh readiness database may sit behind an existing durable row cursor,
// and inventing historical pending periods would turn a normal restart into
// false degraded reports. Keeping the next candidate is necessary because a
// row can arrive at a period boundary before the next timer tick.
func periodWindows(now time.Time, frequency string) ([]periodWindow, error) {
	times, err := report.RecentDatasetTimes(strings.TrimSpace(frequency), now.UTC(), 1)
	if err != nil {
		return nil, err
	}
	if len(times) != 1 {
		return nil, fmt.Errorf("frequency %q did not produce a period boundary", frequency)
	}
	canonical, err := report.NormalizeDatasetFrequency(strings.TrimSpace(frequency))
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(canonical[:len(canonical)-1])
	if err != nil || count <= 0 {
		return nil, fmt.Errorf("frequency %q produced invalid period interval", frequency)
	}
	var next time.Time
	switch canonical[len(canonical)-1] {
	case 'm':
		next = times[0].Add(time.Duration(count) * time.Minute)
	case 'H':
		next = times[0].Add(time.Duration(count) * time.Hour)
	case 'D':
		next = times[0].AddDate(0, 0, count)
	case 'W':
		next = times[0].AddDate(0, 0, 7*count)
	case 'M':
		next = times[0].AddDate(0, count, 0)
	case 'Y':
		next = times[0].AddDate(count, 0, 0)
	default:
		return nil, fmt.Errorf("frequency %q cannot be aligned to a Storage period", frequency)
	}
	var nextNext time.Time
	switch canonical[len(canonical)-1] {
	case 'm':
		nextNext = next.Add(time.Duration(count) * time.Minute)
	case 'H':
		nextNext = next.Add(time.Duration(count) * time.Hour)
	case 'D':
		nextNext = next.AddDate(0, 0, count)
	case 'W':
		nextNext = next.AddDate(0, 0, 7*count)
	case 'M':
		nextNext = next.AddDate(0, count, 0)
	case 'Y':
		nextNext = next.AddDate(count, 0, 0)
	default:
		return nil, fmt.Errorf("frequency %q cannot be aligned to a Storage period", frequency)
	}
	return []periodWindow{
		{PeriodTime: times[0], CloseAt: next},
		{PeriodTime: next, CloseAt: nextNext},
	}, nil
}

type periodWindow struct {
	PeriodTime time.Time
	CloseAt    time.Time
}
