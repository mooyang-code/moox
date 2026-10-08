// Package frequency defines the canonical time-series frequencies shared by
// Storage, Collector, Factor and the CLI. Storage accepts only canonical
// values; aliases are normalized once at input boundaries such as task
// configuration, and exchange API codes stay inside the source adapters.
package frequency

import (
	"fmt"
	"strings"
	"time"
)

// Frequency is a canonical bar length such as "1m" or "1mo".
type Frequency string

const (
	Second30 Frequency = "30s"
	Minute1  Frequency = "1m"
	Minute5  Frequency = "5m"
	Minute15 Frequency = "15m"
	Minute30 Frequency = "30m"
	Hour1    Frequency = "1h"
	Hour4    Frequency = "4h"
	Day1     Frequency = "1d"
	Week1    Frequency = "1w"
	Month1   Frequency = "1mo"
)

var all = []Frequency{Second30, Minute1, Minute5, Minute15, Minute30, Hour1, Hour4, Day1, Week1, Month1}

var durations = map[Frequency]time.Duration{
	Second30: 30 * time.Second,
	Minute1:  time.Minute,
	Minute5:  5 * time.Minute,
	Minute15: 15 * time.Minute,
	Minute30: 30 * time.Minute,
	Hour1:    time.Hour,
	Hour4:    4 * time.Hour,
	Day1:     24 * time.Hour,
	Week1:    7 * 24 * time.Hour,
}

// aliases maps accepted spellings to canonical values. Matching is exact:
// "1M" is a month (exchange convention) while "1m" is a minute.
var aliases = map[string]Frequency{
	"30S": Second30,
	"1H":  Hour1, "60m": Hour1, "60M": Hour1,
	"4H": Hour4, "240m": Hour4,
	"1D": Day1,
	"1W": Week1,
	"1M": Month1, "1MO": Month1, "1Mo": Month1,
}

// All returns the canonical frequencies from the shortest bar to the longest.
func All() []Frequency {
	return append([]Frequency(nil), all...)
}

// Parse returns the canonical frequency for raw, accepting known aliases.
func Parse(raw string) (Frequency, error) {
	value := strings.TrimSpace(raw)
	if IsCanonical(value) {
		return Frequency(value), nil
	}
	if canonical, ok := aliases[value]; ok {
		return canonical, nil
	}
	return "", fmt.Errorf("unsupported frequency %q; use one of %s", raw, strings.Join(Strings(), ", "))
}

// Normalize returns the canonical string for raw, accepting known aliases.
func Normalize(raw string) (string, error) {
	parsed, err := Parse(raw)
	return string(parsed), err
}

// IsCanonical reports whether raw is already a canonical frequency.
func IsCanonical(raw string) bool {
	for _, frequency := range all {
		if string(frequency) == raw {
			return true
		}
	}
	return false
}

// Strings returns the canonical frequencies as strings.
func Strings() []string {
	values := make([]string, len(all))
	for i, frequency := range all {
		values[i] = string(frequency)
	}
	return values
}

// Duration returns the fixed bar length. A month has no fixed length, so
// Month1 returns 0; use BarEnd for calendar arithmetic.
func (f Frequency) Duration() time.Duration {
	return durations[f]
}

// NominalDuration returns the bar length for tolerances such as freshness
// windows, counting a month as 30 days. Never use it for bar boundaries.
func (f Frequency) NominalDuration() time.Duration {
	if f == Month1 {
		return 30 * 24 * time.Hour
	}
	return f.Duration()
}

// BarEnd returns the exclusive end of the bar that starts at start.
func (f Frequency) BarEnd(start time.Time) time.Time {
	if f == Month1 {
		return start.AddDate(0, 1, 0)
	}
	return start.Add(f.Duration())
}

// String implements fmt.Stringer.
func (f Frequency) String() string { return string(f) }
