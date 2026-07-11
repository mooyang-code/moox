package coverage

import (
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type Session struct {
	TradeDate   string
	Open        time.Time
	Close       time.Time
	DailyAnchor time.Time
}

type Range struct {
	Start, End time.Time
	Buckets    int
}

// ExpectedBuckets expands explicit market sessions. It never infers weekdays
// or holidays, keeping completeness coupled to the Market calendar policy.
func ExpectedBuckets(start, end time.Time, frequency marketdata.Frequency, sessions []Session) ([]time.Time, error) {
	if !end.After(start) {
		return nil, fmt.Errorf("end must be after start")
	}
	if frequency.DurationMinutes() <= 0 {
		return nil, fmt.Errorf("frequency %q has no fixed bucket duration", frequency)
	}
	seen := map[time.Time]bool{}
	result := make([]time.Time, 0)
	for _, session := range sessions {
		if !session.Close.After(session.Open) {
			return nil, fmt.Errorf("session close must be after open")
		}
		if frequency == marketdata.FrequencyDay {
			anchor := session.DailyAnchor.UTC()
			if anchor.IsZero() {
				return nil, fmt.Errorf("daily session anchor is required")
			}
			if !anchor.Before(start) && anchor.Before(end) && !seen[anchor] {
				seen[anchor] = true
				result = append(result, anchor)
			}
			continue
		}
		step := time.Duration(frequency.DurationMinutes()) * time.Minute
		for bucket := session.Open.UTC(); bucket.Add(step).Compare(session.Close.UTC()) <= 0; bucket = bucket.Add(step) {
			if !bucket.Before(start) && bucket.Before(end) && !seen[bucket] {
				seen[bucket] = true
				result = append(result, bucket)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Before(result[j]) })
	return result, nil
}

// MissingRanges compares exact expected buckets with persisted unified keys and
// compacts adjacent missing positions in the expected sequence.
func MissingRanges(expected, present []time.Time) []Range {
	have := make(map[time.Time]bool, len(present))
	for _, value := range present {
		have[value.UTC()] = true
	}
	var result []Range
	start := -1
	flush := func(end int) {
		if start >= 0 {
			result = append(result, Range{Start: expected[start].UTC(), End: expected[end].UTC(), Buckets: end - start + 1})
			start = -1
		}
	}
	for index, value := range expected {
		if !have[value.UTC()] {
			if start < 0 {
				start = index
			}
		} else if start >= 0 {
			flush(index - 1)
		}
	}
	if start >= 0 {
		flush(len(expected) - 1)
	}
	return result
}
