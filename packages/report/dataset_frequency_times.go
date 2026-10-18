package report

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
)

// RecentDatasetTimes returns canonical Storage row times from newest to oldest.
func RecentDatasetTimes(raw string, now time.Time, limit int) ([]time.Time, error) {
	parsed, err := frequency.Parse(raw)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, fmt.Errorf("recent dataset time limit must be positive")
	}
	at := datasetTimeFloor(now.UTC(), parsed)
	result := make([]time.Time, 0, limit)
	for range limit {
		result = append(result, at)
		switch parsed {
		case frequency.Week1:
			at = at.AddDate(0, 0, -7)
		case frequency.Month1:
			at = at.AddDate(0, -1, 0)
		default:
			at = at.Add(-parsed.Duration())
		}
	}
	return result, nil
}

// datasetTimeFloor returns the start of the bar containing now. Weeks start
// on Monday and months on the first day; other bars align to the Unix epoch.
func datasetTimeFloor(now time.Time, parsed frequency.Frequency) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch parsed {
	case frequency.Week1:
		return day.AddDate(0, 0, -int((day.Weekday()+6)%7))
	case frequency.Month1:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return now.Truncate(parsed.Duration())
	}
}
