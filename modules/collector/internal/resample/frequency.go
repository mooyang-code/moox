package resample

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

const (
	maxFixedFrequency = 30 * 24 * time.Hour
	maxSourceBars     = 10_080
)

// FixedFrequency is one fixed-duration, whole-minute Storage frequency.
type FixedFrequency struct {
	Storage  string
	Slug     string
	Duration time.Duration
}

// ParseFixedFrequency parses fixed m/h/d periods and returns their canonical
// Storage and Dataset-name forms.
func ParseFixedFrequency(raw string) (FixedFrequency, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 {
		return FixedFrequency{}, fmt.Errorf("fixed frequency must be a positive whole-minute period")
	}

	count, err := strconv.ParseInt(raw[:len(raw)-1], 10, 64)
	if err != nil || count <= 0 {
		return FixedFrequency{}, fmt.Errorf("fixed frequency must be a positive whole-minute period")
	}

	var minutes int64
	switch raw[len(raw)-1] {
	case 'm':
		minutes = count
	case 'h', 'H':
		if count > int64(maxFixedFrequency/time.Hour) {
			return FixedFrequency{}, fmt.Errorf("fixed frequency must not exceed 30 days")
		}
		minutes = count * 60
	case 'd', 'D':
		if count > int64(maxFixedFrequency/(24*time.Hour)) {
			return FixedFrequency{}, fmt.Errorf("fixed frequency must not exceed 30 days")
		}
		minutes = count * 24 * 60
	default:
		return FixedFrequency{}, fmt.Errorf("fixed frequency supports only m, h, and d units")
	}
	if minutes <= 0 || minutes > int64(maxFixedFrequency/time.Minute) {
		return FixedFrequency{}, fmt.Errorf("fixed frequency must not exceed 30 days")
	}

	duration := time.Duration(minutes) * time.Minute
	frequency := FixedFrequency{Duration: duration}
	switch {
	case duration%(24*time.Hour) == 0:
		count = int64(duration / (24 * time.Hour))
		frequency.Storage = strconv.FormatInt(count, 10) + "D"
		frequency.Slug = strconv.FormatInt(count, 10) + "d"
	case duration%time.Hour == 0:
		count = int64(duration / time.Hour)
		frequency.Storage = strconv.FormatInt(count, 10) + "H"
		frequency.Slug = strconv.FormatInt(count, 10) + "h"
	default:
		count = int64(duration / time.Minute)
		frequency.Storage = strconv.FormatInt(count, 10) + "m"
		frequency.Slug = frequency.Storage
	}
	return frequency, nil
}

// ValidateResamplePair verifies that a target bucket expands to a bounded,
// integral number of source bars.
func ValidateResamplePair(source, target FixedFrequency) error {
	if err := validateFixedFrequency(source); err != nil {
		return fmt.Errorf("source frequency: %w", err)
	}
	if err := validateFixedFrequency(target); err != nil {
		return fmt.Errorf("target frequency: %w", err)
	}
	if target.Duration <= source.Duration {
		return fmt.Errorf("target frequency must be greater than source frequency")
	}
	if target.Duration%source.Duration != 0 {
		return fmt.Errorf("target frequency must be an integer multiple of source frequency")
	}
	if target.Duration/source.Duration > maxSourceBars {
		return fmt.Errorf("target bucket must not contain more than %d source bars", maxSourceBars)
	}
	return nil
}

// BucketAt returns the latest fully closed target bucket at effectiveNow on
// the fixed grid anchored at origin.
func BucketAt(effectiveNow, origin time.Time, target FixedFrequency) (start, end time.Time) {
	if validateFixedFrequency(target) != nil || effectiveNow.IsZero() || origin.IsZero() {
		return time.Time{}, time.Time{}
	}
	elapsed := effectiveNow.Sub(origin)
	periods := elapsed / target.Duration
	if elapsed%target.Duration < 0 {
		periods--
	}
	end = origin.Add(periods * target.Duration).UTC()
	return end.Add(-target.Duration), end
}

// EpochBucketStart returns the start of the epoch-UTC bucket containing at.
// Keeping this operation next to BucketAt prevents validation and runtime from
// silently adopting different alignment rules for non-standard periods.
func EpochBucketStart(at time.Time, target FixedFrequency) (time.Time, error) {
	if err := validateFixedFrequency(target); err != nil {
		return time.Time{}, err
	}
	at = at.UTC()
	if at.IsZero() {
		return time.Time{}, fmt.Errorf("bucket time is required")
	}
	start, end := BucketAt(at.Add(time.Nanosecond), time.Unix(0, 0).UTC(), target)
	if start.IsZero() || end.IsZero() {
		return time.Time{}, fmt.Errorf("bucket time cannot be aligned")
	}
	return start, nil
}

// IsEpochBucketAligned reports whether at is exactly on the epoch-UTC grid.
func IsEpochBucketAligned(at time.Time, target FixedFrequency) bool {
	if at.IsZero() || validateFixedFrequency(target) != nil {
		return false
	}
	return domain.IsEpochTimeAligned(at, target.Duration)
}

func validateFixedFrequency(frequency FixedFrequency) error {
	parsed, err := ParseFixedFrequency(frequency.Storage)
	if err != nil {
		return err
	}
	if parsed != frequency {
		return fmt.Errorf("frequency must use canonical Storage and slug values")
	}
	return nil
}
