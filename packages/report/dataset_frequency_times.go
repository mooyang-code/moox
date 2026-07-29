package report

import (
	"fmt"
	"strconv"
	"time"
)

// RecentDatasetTimes returns canonical Storage row times from newest to oldest.
func RecentDatasetTimes(frequency string, now time.Time, limit int) ([]time.Time, error) {
	canonical, _, err := parseDatasetFrequency(frequency)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, fmt.Errorf("recent dataset time limit must be positive")
	}
	step, err := strconv.Atoi(canonical[:len(canonical)-1])
	if err != nil || step <= 0 {
		return nil, fmt.Errorf("frequency must be a positive duration")
	}
	suffix := canonical[len(canonical)-1]
	at := datasetTimeFloor(now.UTC(), step, suffix)
	result := make([]time.Time, 0, limit)
	for range limit {
		result = append(result, at)
		switch suffix {
		case 'm':
			at = at.Add(-time.Duration(step) * time.Minute)
		case 'H':
			at = at.Add(-time.Duration(step) * time.Hour)
		case 'D':
			at = at.AddDate(0, 0, -step)
		case 'W':
			at = at.AddDate(0, 0, -7*step)
		case 'M':
			at = at.AddDate(0, -step, 0)
		case 'Y':
			at = at.AddDate(-step, 0, 0)
		}
	}
	return result, nil
}

func datasetTimeFloor(now time.Time, step int, suffix byte) time.Time {
	switch suffix {
	case 'm':
		seconds := int64(step) * int64(time.Minute/time.Second)
		return time.Unix(now.Unix()/seconds*seconds, 0).UTC()
	case 'H':
		seconds := int64(step) * int64(time.Hour/time.Second)
		return time.Unix(now.Unix()/seconds*seconds, 0).UTC()
	case 'D':
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		days := int(day.Sub(time.Unix(0, 0).UTC()) / (24 * time.Hour))
		return day.AddDate(0, 0, -(days % step))
	case 'W':
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		monday := day.AddDate(0, 0, -int((day.Weekday()+6)%7))
		anchor := time.Date(1970, time.January, 5, 0, 0, 0, 0, time.UTC)
		weeks := int(monday.Sub(anchor) / (7 * 24 * time.Hour))
		return monday.AddDate(0, 0, -7*(weeks%step))
	case 'M':
		months := now.Year()*12 + int(now.Month()) - 1
		months -= months % step
		return time.Date(months/12, time.Month(months%12+1), 1, 0, 0, 0, 0, time.UTC)
	case 'Y':
		year := (now.Year()-1)/step*step + 1
		return time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	default:
		return time.Time{}
	}
}
