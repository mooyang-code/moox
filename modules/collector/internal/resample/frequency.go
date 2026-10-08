package resample

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/packages/frequency"
)

const maxSourceBars = 10_080

// FixedFrequency is one canonical frequency that can be resampled: a fixed,
// whole-minute bar from 1m to 1d.
type FixedFrequency struct {
	Storage  string
	Duration time.Duration
}

// ParseFixedFrequency parses a canonical frequency that can be resampled.
func ParseFixedFrequency(raw string) (FixedFrequency, error) {
	duration, err := domain.ResampleFrequencyDuration(raw)
	if err != nil {
		return FixedFrequency{}, err
	}
	parsed, _ := frequency.Parse(raw)
	return FixedFrequency{Storage: string(parsed), Duration: duration}, nil
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
	// Resampleable frequencies run from 1m to 1d, so a larger target is always
	// a whole multiple of the source and holds at most 1440 source bars.
	if target.Duration <= source.Duration {
		return fmt.Errorf("target frequency must be greater than source frequency")
	}
	return nil
}

// ValidateTaskParams validates the resample runtime contract after the result
// Dataset identity has been assigned from space_id and task_id.
func ValidateTaskParams(params *domain.CollectParams) error {
	if params == nil {
		return fmt.Errorf("resample collect params are required")
	}
	if strings.TrimSpace(params.SourceDatasetID) == "" {
		return fmt.Errorf("source_dataset_id is required")
	}
	if strings.TrimSpace(params.TargetDatasetID) == "" {
		return fmt.Errorf("target_dataset_id is required")
	}
	if strings.TrimSpace(params.SourceDatasetID) == strings.TrimSpace(params.TargetDatasetID) {
		return fmt.Errorf("source and target Dataset IDs must differ")
	}
	if strings.TrimSpace(params.SourceSeriesTag) == "" {
		return fmt.Errorf("source_series_tag is required")
	}
	if strings.TrimSpace(params.Alignment) != AlignmentEpochUTC {
		return fmt.Errorf("alignment must be %s", AlignmentEpochUTC)
	}
	source, err := ParseFixedFrequency(params.SourceFrequency)
	if err != nil {
		return fmt.Errorf("source_frequency: %w", err)
	}
	target, err := ParseFixedFrequency(params.TargetFrequency)
	if err != nil {
		return fmt.Errorf("target_frequency: %w", err)
	}
	if err := ValidateResamplePair(source, target); err != nil {
		return err
	}
	return ValidateTargetDatasetID(params.TargetDatasetID)
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

func validateFixedFrequency(frequency FixedFrequency) error {
	parsed, err := ParseFixedFrequency(frequency.Storage)
	if err != nil {
		return err
	}
	if parsed != frequency {
		return fmt.Errorf("frequency must use the canonical Storage value")
	}
	return nil
}
